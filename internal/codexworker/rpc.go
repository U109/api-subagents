// Package codexworker 使用隔离的 Codex App Server 执行明文任务，不依赖原生子代理的上下文转交。
package codexworker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/U109/api-subagents/internal/platform"
	"github.com/U109/api-subagents/internal/shared"
)

type rpcClient struct {
	input      io.WriteCloser
	output     io.ReadCloser
	encoder    *json.Encoder
	writeMu    sync.Mutex
	incoming   chan shared.Object
	errors     chan error
	stop       chan struct{}
	wait       chan struct{}
	readerDone chan struct{}
	kill       func()
	seq        int
	event      func(string, shared.Object) error
}

// startRPC 以有界逐行 JSON 读取协议；不把 stderr 或供应商原始日志混入 MCP stdout。
func startRPC(cmd *exec.Cmd, event func(string, shared.Object) error) (*rpcClient, error) {
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		return nil, err
	}
	cmd.Stderr = io.Discard
	kill, err := platform.StartWorkerProcess(cmd)
	if err != nil {
		input.Close()
		output.Close()
		return nil, errors.New("无法启动受管理的 Codex worker，请检查原生 CLI 和沙箱环境。")
	}
	c := &rpcClient{input: input, output: output, encoder: json.NewEncoder(input), incoming: make(chan shared.Object, 64), errors: make(chan error, 1), stop: make(chan struct{}), wait: make(chan struct{}), readerDone: make(chan struct{}), kill: kill, event: event}
	go func() { <-c.readerDone; _ = cmd.Wait(); close(c.wait) }()
	go c.read()
	return c, nil
}

// read 对单条协议消息设 8 MB 上限，退出时唤醒等待者；背压不会造成关闭时的 goroutine 泄漏。
func (c *rpcClient) read() {
	defer close(c.readerDone)
	scanner := bufio.NewScanner(c.output)
	scanner.Buffer(make([]byte, 65536), 8*1024*1024)
	for scanner.Scan() {
		var value shared.Object
		if json.Unmarshal(scanner.Bytes(), &value) != nil || value == nil {
			c.errors <- errors.New("Codex App Server 返回了无效协议消息。")
			return
		}
		select {
		case c.incoming <- value:
		case <-c.stop:
			return
		}
	}
	c.errors <- errors.New("Codex App Server 在任务结束前断开，或协议消息超过限制。")
}

// send 序列化 stdin 写入，调用方关闭客户端可解除进程不再读取时的阻塞。
func (c *rpcClient) send(value shared.Object) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.encoder.Encode(value)
}

// receive 优先消费已排队消息，避免进程退出事件覆盖已经收到的最终结果。
func (c *rpcClient) receive(ctx context.Context) (shared.Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, context.Cause(ctx)
	}
	select {
	case value := <-c.incoming:
		return value, nil
	default:
	}
	select {
	case value := <-c.incoming:
		return value, nil
	case err := <-c.errors:
		select {
		case value := <-c.incoming:
			c.errors <- err
			return value, nil
		default:
			return nil, err
		}
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}

// dispatch 对全部服务端交互请求失败关闭，不自动批准提权、网络访问或交互输入。
func (c *rpcClient) dispatch(value shared.Object) error {
	method := shared.Str(value["method"])
	if method == "" {
		return nil
	}
	if value["id"] != nil {
		_ = c.send(shared.Object{"id": value["id"], "error": shared.Object{"code": -32601, "message": "Unattended worker cannot approve permissions or supply interactive input"}})
		return &shared.OpError{Code: "WORKER_INTERACTION_REQUIRED", Message: "子代理请求审批或交互输入，已停止；请由主任务处理，禁止自动提权。"}
	}
	return c.event(method, shared.Obj(value["params"]))
}

// call 在等待响应时继续处理通知和权限请求，所有请求只发送一次，不重试执行。
func (c *rpcClient) call(ctx context.Context, method string, params shared.Object) (shared.Object, error) {
	c.seq++
	id := c.seq
	if err := c.send(shared.Object{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		value, err := c.receive(ctx)
		if err != nil {
			return nil, err
		}
		if value["method"] == nil && shared.Int(value["id"]) == id {
			if value["error"] != nil {
				return nil, errors.New("Codex App Server 拒绝 " + method + "：" + shared.Clip(shared.Str(shared.Obj(value["error"])["message"]), 1000))
			}
			return shared.Obj(value["result"]), nil
		}
		if err := c.dispatch(value); err != nil {
			return nil, err
		}
	}
}

// close 先请求中断并关闭输入，最多等待两秒；随后终止整个进程树，包括遗留的命令进程。
func (c *rpcClient) close(thread, turn string) {
	close(c.stop)
	// 防止无响应的 stdin 让清理无限等待；进程树终止后写入与 Wait 都会退出。
	timer := time.AfterFunc(2*time.Second, func() { c.kill(); _ = c.input.Close() })
	if thread != "" && turn != "" {
		_ = c.send(shared.Object{"id": 1000000, "method": "turn/interrupt", "params": shared.Object{"threadId": thread, "turnId": turn}})
	}
	_ = c.input.Close()
	select {
	case <-c.wait:
	case <-time.After(2 * time.Second):
	}
	c.kill()
	_ = c.output.Close()
	<-c.wait
	<-c.readerDone
	timer.Stop()
}

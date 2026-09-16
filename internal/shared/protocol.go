package shared

type OpError struct {
	Code    string
	Message string
}

// Error 给调用者提供固定、可读的边界错误，不带网络请求的密钥或正文。
func (e *OpError) Error() string { return e.Message }

type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Schema      Object `json:"inputSchema"`
}

type Call struct {
	ID     string
	Name   string
	Args   any
	Output any
}

type Reply struct {
	Text  string
	Calls []Call
}

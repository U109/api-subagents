package config

// EffectiveParameters 返回运行用的参数副本；显式关闭覆盖时使用源码默认值，旧配置缺省开关仍沿用原参数。
// 原草稿中的输出、响应与超时值不被改写，重新开启后仍可恢复；模型独立额度和思考偏好不受影响。
func (p Profile) EffectiveParameters() Profile {
	if p.AdvancedOverride != nil && !*p.AdvancedOverride {
		p.MaxTokens = 4096
		p.Stream = true
		p.FirstTimeout = 180
		p.IdleTimeout = 120
		p.TaskTimeout = 15
	}
	return p
}

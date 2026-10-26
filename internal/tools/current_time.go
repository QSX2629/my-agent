package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	// 将时区数据包含进程序，方便在 Windows 等环境使用。
	_ "time/tzdata"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type CurrentTime struct{}

func NewCurrentTime() *CurrentTime {
	return &CurrentTime{}
}

// Info 告诉模型：工具叫什么、有什么用途、接收什么参数。
func (t *CurrentTime) Info(
	_ context.Context,
) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "get_current_time",
		Desc: "查询当前北京时间，时区为 Asia/Shanghai。" +
			"当用户询问当前日期或当前时间时，使用这个工具获取真实时间。",
		ParamsOneOf: schema.NewParamsOneOfByParams(
			map[string]*schema.ParameterInfo{},
		),
	}, nil
}

// InvokableRun 是真正执行工具的 Go 代码。
func (t *CurrentTime) InvokableRun(
	ctx context.Context,
	arguments string,
	_ ...tool.Option,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	arguments = strings.TrimSpace(arguments)

	// 无参数工具允许模型发送空字符串或 {}。
	if arguments == "" {
		arguments = "{}"
	}

	var parameters map[string]json.RawMessage

	if err := json.Unmarshal(
		[]byte(arguments),
		&parameters,
	); err != nil {
		return "", errors.New("invalid current time arguments")
	}

	if parameters == nil || len(parameters) != 0 {
		return "", errors.New(
			"current time tool does not accept parameters",
		)
	}

	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return "", err
	}

	result := struct {
		Timestamp string `json:"timestamp"`
		Timezone  string `json:"timezone"`
	}{
		Timestamp: time.Now().In(location).Format(time.RFC3339),
		Timezone:  "Asia/Shanghai",
	}

	body, err := json.Marshal(result)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

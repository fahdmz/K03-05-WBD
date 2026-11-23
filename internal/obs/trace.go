package obs

import "context"

func InjectTrace(ctx context.Context) map[string]string {
	return map[string]string{}
}

func ExtractTrace(ctx context.Context, carrier map[string]string) context.Context {
	return ctx
}

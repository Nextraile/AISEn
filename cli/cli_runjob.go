package cli

import "context"

// RunJob adalah dispatcher tipis untuk dipakai REST API (menghindari
// siklus import: internal/api tidak boleh meng-import cli).
func RunJob(ctx context.Context, typ string, args []string) error {
	switch typ {
	case "harvest":
		return CmdHarvest(ctx, args)
	case "clean":
		return CmdClean(ctx, args)
	case "normalize":
		return CmdNormalize(ctx, args)
	case "vectorize":
		return CmdVectorize(ctx, args)
	case "index":
		return CmdIndex(ctx, args)
	case "pipeline":
		return CmdPipeline(ctx, args)
	}
	return ErrUsage
}

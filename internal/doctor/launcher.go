package doctor

type resolvedProviderCommand struct {
	Executable validatedPath
	Entrypoint *validatedPath
	PrefixArgs []string
}

func nativeProviderCommand(executable validatedPath) resolvedProviderCommand {
	return resolvedProviderCommand{Executable: executable}
}

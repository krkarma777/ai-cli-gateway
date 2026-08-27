package app

import (
	"context"
	"errors"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/doctor"
	"github.com/krkarma777/ai-cli-gateway/internal/provider"
)

var errDiagnosisSourceClose = errors.New("diagnosis source close failed")

func diagnose(
	ctx context.Context,
	configPath string,
	deps Dependencies,
) (doctor.Diagnosis, error) {
	return diagnoseWithStartup(ctx, configPath, deps, productionStartupDependencies())
}

func diagnoseWithStartup(
	ctx context.Context,
	configPath string,
	deps Dependencies,
	startup startupDependencies,
) (diagnosis doctor.Diagnosis, resultErr error) {
	if startup.LoadConfigSource == nil {
		return doctor.Diagnosis{}, ErrStartup
	}
	source, err := startup.LoadConfigSource(configPath)
	if err != nil || nilLike(source) {
		if !nilLike(source) {
			_ = source.Close()
		}
		return doctor.Diagnosis{}, ErrConfigInvalid
	}
	defer func() {
		if source.Close() == nil {
			return
		}
		if resultErr == nil {
			resultErr = errDiagnosisSourceClose
		}
	}()

	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil || !validDoctorDependencies(deps) {
		return doctor.Diagnosis{}, ErrStartup
	}
	cfg := source.Config()
	adapters, ok := selectAdapters(cfg, deps.Adapters)
	if !ok {
		return doctor.Diagnosis{}, ErrStartup
	}
	executable, err := deps.GatewayExecutable()
	if err != nil {
		return doctor.Diagnosis{}, ErrStartup
	}
	diagnosis, err = doctor.Run(ctx, cfg, doctor.Dependencies{
		Adapters:           adapters,
		ConfigIdentity:     source.FileInfo(),
		LookupEnv:          deps.LookupEnv,
		LoadGatewayKey:     startup.LoadGatewayKey,
		LookupExecutable:   deps.LookupExecutable,
		NewRuntimeID:       deps.NewRuntimeID,
		OpenRoot:           deps.OpenRoot,
		Janitor:            deps.Janitor,
		CloseRoot:          deps.CloseRoot,
		NewProbeController: deps.NewProbeController,
		GatewayExecutable:  executable,
	})
	if err != nil {
		if diagnosis.RuntimeRoot != nil {
			_ = deps.CloseRoot(diagnosis.RuntimeRoot)
		}
		return doctor.Diagnosis{}, ErrStartup
	}
	if startup.postDiagnosis != nil {
		startup.postDiagnosis()
	}
	return diagnosis, nil
}

func selectedProvidersReady(
	report doctor.Report,
	selected []core.ProviderName,
) bool {
	if len(selected) == 0 || !report.CoreReady() {
		return false
	}
	rows := report.Providers()
	byName := make(map[core.ProviderName]doctor.Provider, len(rows))
	for _, row := range rows {
		if row.Name == "" {
			return false
		}
		if _, duplicate := byName[row.Name]; duplicate {
			return false
		}
		byName[row.Name] = row
	}
	seen := make(map[core.ProviderName]struct{}, len(selected))
	for _, name := range selected {
		if name == "" {
			return false
		}
		if _, duplicate := seen[name]; duplicate {
			return false
		}
		seen[name] = struct{}{}
		row, present := byName[name]
		if !present || row.Status != provider.HealthReady {
			return false
		}
	}
	return true
}

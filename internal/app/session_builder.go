package app

import (
	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/process"
)

func sessionBuilder(options PickerOptions, dependencies *Dependencies) (*candidate.Builder, error) {
	path := dependencies.ZoxidePath
	if path == "" {
		path = "zoxide"
	}
	environment := process.SanitizeEnv(dependencies.Environment, nil)
	newCache := func() (*candidate.ZoxideCache, error) {
		return candidate.NewZoxideCache(dependencies.ProcessRunner, path, environment, options.ZoxideTimeout)
	}
	if options.ZoxidePolicy == candidate.ZoxideCached {
		cache, err := newCache()
		if err != nil {
			return nil, err
		}
		dependencies.CandidateBuilder.ConfigureCached(cache)
	} else {
		dependencies.CandidateBuilder.ConfigureFresh(newCache)
	}
	return &dependencies.CandidateBuilder, nil
}

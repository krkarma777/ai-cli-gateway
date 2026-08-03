package core

import (
	"errors"
	"regexp"
	"sort"
	"unicode"
	"unicode/utf8"
)

var aliasPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

var (
	errInvalidModelAlias    = errors.New("model alias is invalid")
	errDuplicateModelAlias  = errors.New("model alias is duplicated")
	errUnknownModelProvider = errors.New("model provider is unknown")
	errInvalidProviderModel = errors.New("provider model is invalid")
)

// Registry is an immutable alias index over configured models.
type Registry struct {
	byID   map[string]Model
	models []Model
}

// NewRegistry validates and defensively copies configured models.
func NewRegistry(models []Model) (*Registry, error) {
	copiedModels := append([]Model(nil), models...)
	byID := make(map[string]Model, len(copiedModels))

	for _, model := range copiedModels {
		if !aliasPattern.MatchString(model.ID) {
			return nil, errInvalidModelAlias
		}
		if _, exists := byID[model.ID]; exists {
			return nil, errDuplicateModelAlias
		}
		if !knownProvider(model.Provider) {
			return nil, errUnknownModelProvider
		}
		if err := ValidateProviderModel(model.ProviderModel); err != nil {
			return nil, err
		}
		byID[model.ID] = model
	}

	sort.Slice(copiedModels, func(i, j int) bool {
		return copiedModels[i].ID < copiedModels[j].ID
	})
	return &Registry{byID: byID, models: copiedModels}, nil
}

// ValidateProviderModel validates a trusted provider CLI model argument.
func ValidateProviderModel(value string) error {
	if value == "" ||
		len(value) > 256 ||
		value[0] == '-' ||
		!utf8.ValidString(value) {
		return errInvalidProviderModel
	}
	for _, r := range value {
		if !unicode.IsPrint(r) {
			return errInvalidProviderModel
		}
	}
	return nil
}

// Resolve returns the model configured for alias.
func (r *Registry) Resolve(alias string) (Model, bool) {
	if r == nil {
		return Model{}, false
	}
	model, ok := r.byID[alias]
	return model, ok
}

// Models returns a defensive copy sorted by public alias.
func (r *Registry) Models() []Model {
	if r == nil {
		return nil
	}
	return append([]Model(nil), r.models...)
}

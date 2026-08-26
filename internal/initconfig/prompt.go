package initconfig

import (
	"context"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

// ReviewDecision selects the next step after an interactive review.
type ReviewDecision uint8

// Supported interactive review decisions.
const (
	ReviewConfirm ReviewDecision = iota + 1
	ReviewBack
	ReviewDecline
)

// ProviderSelectionRequest describes the provider-selection prompt state.
type ProviderSelectionRequest struct {
	Initial  []core.ProviderName
	Existing []core.ProviderName
}

// ProviderSelectionResponse contains the selected providers and decision.
type ProviderSelectionResponse struct {
	Providers []core.ProviderName
	Decision  ReviewDecision
}

// CollectRequest describes the values available to an interactive collector.
type CollectRequest struct {
	Initial  Options
	Existing *config.Config
	// Discovery is nil when CollectGatewayKey revisits only the Gateway key
	// path/auth group. Prompt implementations must leave provider/model groups
	// untouched in that convention.
	Discovery map[core.ProviderName]ProviderDiscovery
}

// CollectResponse contains collected options and optional selection navigation.
type CollectResponse struct {
	Options         Options
	BackToSelection bool
}

// CollisionChoice selects how an existing configuration collision is resolved.
type CollisionChoice uint8

// Supported collision choices.
const (
	CollisionReplace CollisionChoice = iota + 1
	CollisionKeepExisting
)

// CollisionDecision resolves one provider or model collision.
type CollisionDecision struct {
	Target DiffTarget
	Name   string
	Choice CollisionChoice
}

// ReviewRequest contains the diff and collisions shown for confirmation.
type ReviewRequest struct {
	Diff       SemanticDiff
	Collisions []Collision
}

// ReviewResponse contains the review decision and collision choices.
type ReviewResponse struct {
	Decision   ReviewDecision
	Collisions []CollisionDecision
}

// KeyConfirmationKind identifies a Gateway-key action requiring confirmation.
type KeyConfirmationKind uint8

// Supported Gateway-key confirmation kinds.
const (
	ConfirmOrphanReuse KeyConfirmationKind = iota + 1
	ConfirmMissingConfiguredKeyCreation
)

// KeyConfirmationRequest describes one Gateway-key confirmation prompt.
type KeyConfirmationRequest struct {
	Kind KeyConfirmationKind
	Path string
}

// Prompt provides the closed interactive init prompt operations.
type Prompt interface {
	SelectProviders(context.Context, ProviderSelectionRequest) (ProviderSelectionResponse, error)
	Collect(context.Context, CollectRequest) (CollectResponse, error)
	Review(context.Context, ReviewRequest) (ReviewResponse, error)
	ConfirmKeyAction(context.Context, KeyConfirmationRequest) (ReviewDecision, error)
}

// DiscoverSelected discovers suggestions for the currently selected providers.
type DiscoverSelected func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error)

// DiffPresenter renders one semantic diff before confirmation.
type DiffPresenter func(SemanticDiff) error

// CollectFocus limits which interactive fields are revisited.
type CollectFocus uint8

// Supported interactive collection scopes.
const (
	CollectAll CollectFocus = iota + 1
	CollectGatewayKey
)

// ResumeState retains validated interactive options for a back-navigation step.
type ResumeState struct {
	options Options
}

// InteractiveResult contains a planned mutation and its interactive decision.
type InteractiveResult struct {
	Plan     PlanningResult
	Decision ReviewDecision
	Resume   *ResumeState
}

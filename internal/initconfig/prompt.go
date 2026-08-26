package initconfig

import (
	"context"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

type ReviewDecision uint8

const (
	ReviewConfirm ReviewDecision = iota + 1
	ReviewBack
	ReviewDecline
)

type ProviderSelectionRequest struct {
	Initial  []core.ProviderName
	Existing []core.ProviderName
}

type ProviderSelectionResponse struct {
	Providers []core.ProviderName
	Decision  ReviewDecision
}

type CollectRequest struct {
	Initial  Options
	Existing *config.Config
	// Discovery is nil when CollectGatewayKey revisits only the Gateway key
	// path/auth group. Prompt implementations must leave provider/model groups
	// untouched in that convention.
	Discovery map[core.ProviderName]ProviderDiscovery
}

type CollectResponse struct {
	Options         Options
	BackToSelection bool
}

type CollisionChoice uint8

const (
	CollisionReplace CollisionChoice = iota + 1
	CollisionKeepExisting
)

type CollisionDecision struct {
	Target DiffTarget
	Name   string
	Choice CollisionChoice
}

type ReviewRequest struct {
	Diff       SemanticDiff
	Collisions []Collision
}

type ReviewResponse struct {
	Decision   ReviewDecision
	Collisions []CollisionDecision
}

type KeyConfirmationKind uint8

const (
	ConfirmOrphanReuse KeyConfirmationKind = iota + 1
	ConfirmMissingConfiguredKeyCreation
)

type KeyConfirmationRequest struct {
	Kind KeyConfirmationKind
	Path string
}

type Prompt interface {
	SelectProviders(context.Context, ProviderSelectionRequest) (ProviderSelectionResponse, error)
	Collect(context.Context, CollectRequest) (CollectResponse, error)
	Review(context.Context, ReviewRequest) (ReviewResponse, error)
	ConfirmKeyAction(context.Context, KeyConfirmationRequest) (ReviewDecision, error)
}

type DiscoverSelected func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error)
type DiffPresenter func(SemanticDiff) error

type CollectFocus uint8

const (
	CollectAll CollectFocus = iota + 1
	CollectGatewayKey
)

type ResumeState struct {
	options Options
}

type InteractiveResult struct {
	Plan     PlanningResult
	Decision ReviewDecision
	Resume   *ResumeState
}

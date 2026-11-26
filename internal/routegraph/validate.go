package routegraph

import (
	"fmt"
	"strings"

	"github.com/tokenplatform/publisher/internal/types"
)

type ValidationOptions struct {
	Prefix                        string
	ProductID                     string
	KnownProviderIDs              map[string]struct{}
	ProvidersWithCredentials      map[string]struct{}
	RouteProvidersByModel         map[string]map[string]struct{}
	ProductAllowedProviderIDs     []string
	ProductAllowedChannelIDs      []string
	ProductAllowedModels          []string
	CheckTargetPolicyIntersection bool
}

func Validate(graph *types.ProductRouteGraph, opts ValidationOptions) error {
	if graph == nil {
		return nil
	}
	prefix := opts.Prefix
	if prefix == "" {
		prefix = "route_graph"
	}
	if graph.SchemaVersion == 0 {
		return fmt.Errorf("%s.schema_version required", prefix)
	}
	if err := validateRouteTarget(prefix+".default_target", graph.DefaultTarget.TargetType, graph.DefaultTarget.TargetID); err != nil {
		return err
	}
	if graph.Groups == nil {
		return fmt.Errorf("%s.groups required", prefix)
	}
	if len(graph.ProductChannels) == 0 {
		return fmt.Errorf("%s.product_channels required", prefix)
	}
	if graph.Assignments == nil {
		return fmt.Errorf("%s.assignments required", prefix)
	}

	knownProviderIDs := opts.KnownProviderIDs
	if knownProviderIDs == nil && len(opts.ProductAllowedProviderIDs) > 0 {
		knownProviderIDs = stringSet(opts.ProductAllowedProviderIDs)
	}
	knownChannelIDs := stringSet(opts.ProductAllowedChannelIDs)
	checkChannelIDs := len(knownChannelIDs) > 0

	channelsByID := make(map[string]types.ProductRouteChannel, len(graph.ProductChannels))
	providersByID := make(map[string]struct{}, len(graph.ProductChannels))
	for i, channel := range graph.ProductChannels {
		label := fmt.Sprintf("%s.product_channels[%d]", prefix, i)
		if strings.TrimSpace(channel.ID) == "" {
			return fmt.Errorf("%s.id required", label)
		}
		if strings.TrimSpace(channel.ChannelID) == "" {
			return fmt.Errorf("%s.channel_id required", label)
		}
		if checkChannelIDs {
			if _, ok := knownChannelIDs[channel.ChannelID]; !ok {
				return fmt.Errorf("%s.channel_id unknown: %s", label, channel.ChannelID)
			}
		}
		providerID := strings.TrimSpace(channel.ProviderID)
		if providerID == "" {
			return fmt.Errorf("%s.provider_id required", label)
		}
		if knownProviderIDs != nil {
			if _, ok := knownProviderIDs[providerID]; !ok {
				return fmt.Errorf("%s.provider_id unknown: %s", label, providerID)
			}
		}
		if opts.ProvidersWithCredentials != nil {
			if _, ok := opts.ProvidersWithCredentials[providerID]; !ok {
				return fmt.Errorf("%s.provider_id has no provider_credentials: %s", label, providerID)
			}
		}
		if channel.AllowedModels == nil {
			return fmt.Errorf("%s.allowed_models required", label)
		}
		if channel.DeniedModels == nil {
			return fmt.Errorf("%s.denied_models required", label)
		}
		if strings.TrimSpace(channel.Status) == "" {
			return fmt.Errorf("%s.status required", label)
		}
		if !allowedValue(channel.Status, "active", "disabled", "testing") {
			return fmt.Errorf("%s.status=%s not allowed", label, channel.Status)
		}
		if _, exists := channelsByID[channel.ID]; exists {
			return fmt.Errorf("%s.id duplicate: %s", label, channel.ID)
		}
		if _, exists := providersByID[providerID]; exists {
			return fmt.Errorf("%s.provider_id duplicate: %s", label, providerID)
		}
		channelsByID[channel.ID] = channel
		providersByID[providerID] = struct{}{}
		for _, model := range channel.AllowedModels {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			if opts.RouteProvidersByModel != nil {
				if _, ok := opts.RouteProvidersByModel[model][providerID]; !ok {
					return fmt.Errorf("%s.provider_id %s missing from model_routes[%s]", label, providerID, model)
				}
			}
		}
	}

	groupsByID := make(map[string]types.ProductRouteGroup, len(graph.Groups))
	for i, group := range graph.Groups {
		label := fmt.Sprintf("%s.groups[%d]", prefix, i)
		if strings.TrimSpace(group.ID) == "" {
			return fmt.Errorf("%s.id required", label)
		}
		if strings.TrimSpace(group.Code) == "" {
			return fmt.Errorf("%s.code required", label)
		}
		if strings.TrimSpace(group.Status) == "" {
			return fmt.Errorf("%s.status required", label)
		}
		if !allowedValue(group.Status, "active", "disabled") {
			return fmt.Errorf("%s.status=%s not allowed", label, group.Status)
		}
		if strings.TrimSpace(group.Strategy) == "" {
			return fmt.Errorf("%s.strategy required", label)
		}
		if !allowedValue(group.Strategy, "weighted_failover", "priority_failover", "round_robin", "cost_first", "latency_first") {
			return fmt.Errorf("%s.strategy=%s not allowed", label, group.Strategy)
		}
		if group.Members == nil {
			return fmt.Errorf("%s.members required", label)
		}
		if len(group.Members) == 0 {
			return fmt.Errorf("%s.members empty", label)
		}
		if _, exists := groupsByID[group.ID]; exists {
			return fmt.Errorf("%s.id duplicate: %s", label, group.ID)
		}
		groupsByID[group.ID] = group
		for j, member := range group.Members {
			memberLabel := fmt.Sprintf("%s.members[%d]", label, j)
			if strings.TrimSpace(member.ProductChannelID) == "" {
				return fmt.Errorf("%s.product_channel_id required", memberLabel)
			}
			channel, ok := channelsByID[member.ProductChannelID]
			if !ok {
				return fmt.Errorf("%s.product_channel_id unknown: %s", memberLabel, member.ProductChannelID)
			}
			if strings.TrimSpace(member.ChannelID) == "" {
				return fmt.Errorf("%s.channel_id required", memberLabel)
			}
			if member.ChannelID != channel.ChannelID {
				return fmt.Errorf("%s.channel_id %s does not match product_channels[%s].channel_id %s", memberLabel, member.ChannelID, member.ProductChannelID, channel.ChannelID)
			}
			providerID := strings.TrimSpace(member.ProviderID)
			if providerID == "" {
				return fmt.Errorf("%s.provider_id required", memberLabel)
			}
			if knownProviderIDs != nil {
				if _, ok := knownProviderIDs[providerID]; !ok {
					return fmt.Errorf("%s.provider_id unknown: %s", memberLabel, providerID)
				}
			}
			if providerID != channel.ProviderID {
				return fmt.Errorf("%s.provider_id %s does not match product_channels[%s].provider_id %s", memberLabel, providerID, member.ProductChannelID, channel.ProviderID)
			}
			if strings.TrimSpace(member.Role) == "" {
				return fmt.Errorf("%s.role required", memberLabel)
			}
			if !allowedValue(member.Role, "primary", "fallback", "shadow") {
				return fmt.Errorf("%s.role=%s not allowed", memberLabel, member.Role)
			}
		}
	}

	if err := validateExistingTarget(prefix+".default_target", graph.DefaultTarget.TargetType, graph.DefaultTarget.TargetID, groupsByID, channelsByID); err != nil {
		return err
	}
	if opts.CheckTargetPolicyIntersection {
		if err := validateTargetPolicyIntersection(prefix+".default_target", graph.DefaultTarget.TargetType, graph.DefaultTarget.TargetID, groupsByID, channelsByID, opts); err != nil {
			return err
		}
	}
	for i, assignment := range graph.Assignments {
		label := fmt.Sprintf("%s.assignments[%d]", prefix, i)
		if strings.TrimSpace(assignment.ID) == "" {
			return fmt.Errorf("%s.id required", label)
		}
		if strings.TrimSpace(assignment.ProductID) == "" {
			return fmt.Errorf("%s.product_id required", label)
		}
		if opts.ProductID != "" && assignment.ProductID != opts.ProductID {
			return fmt.Errorf("%s.product_id %s does not match product_id %s", label, assignment.ProductID, opts.ProductID)
		}
		if strings.TrimSpace(assignment.SubjectType) == "" {
			return fmt.Errorf("%s.subject_type required", label)
		}
		if !allowedValue(assignment.SubjectType, "USER", "USER_SEGMENT") {
			return fmt.Errorf("%s.subject_type=%s not allowed", label, assignment.SubjectType)
		}
		if strings.TrimSpace(assignment.SubjectID) == "" {
			return fmt.Errorf("%s.subject_id required", label)
		}
		if err := validateRouteTarget(label, assignment.TargetType, assignment.TargetID); err != nil {
			return err
		}
		if strings.TrimSpace(assignment.Mode) == "" {
			return fmt.Errorf("%s.mode required", label)
		}
		if !allowedValue(assignment.Mode, "ONLY", "PREFER") {
			return fmt.Errorf("%s.mode=%s not allowed", label, assignment.Mode)
		}
		if err := validateExistingTarget(label, assignment.TargetType, assignment.TargetID, groupsByID, channelsByID); err != nil {
			return err
		}
		if opts.CheckTargetPolicyIntersection {
			if err := validateTargetPolicyIntersection(label, assignment.TargetType, assignment.TargetID, groupsByID, channelsByID, opts); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateRouteTarget(label, targetType, targetID string) error {
	if strings.TrimSpace(targetType) == "" {
		return fmt.Errorf("%s.target_type required", label)
	}
	if !allowedValue(targetType, "GROUP", "PRODUCT_CHANNEL") {
		return fmt.Errorf("%s.target_type=%s not allowed", label, targetType)
	}
	if strings.TrimSpace(targetID) == "" {
		return fmt.Errorf("%s.target_id required", label)
	}
	return nil
}

func validateExistingTarget(label, targetType, targetID string, groupsByID map[string]types.ProductRouteGroup, channelsByID map[string]types.ProductRouteChannel) error {
	switch targetType {
	case "GROUP":
		if _, ok := groupsByID[targetID]; !ok {
			return fmt.Errorf("%s unknown: GROUP/%s", label, targetID)
		}
	case "PRODUCT_CHANNEL":
		if _, ok := channelsByID[targetID]; !ok {
			return fmt.Errorf("%s unknown: PRODUCT_CHANNEL/%s", label, targetID)
		}
	}
	return nil
}

func validateTargetPolicyIntersection(label, targetType, targetID string, groupsByID map[string]types.ProductRouteGroup, channelsByID map[string]types.ProductRouteChannel, opts ValidationOptions) error {
	candidates := targetChannels(targetType, targetID, groupsByID, channelsByID)
	if len(candidates) == 0 {
		return fmt.Errorf("%s provider intersection empty", label)
	}
	providerSet := stringSet(opts.ProductAllowedProviderIDs)
	channelSet := stringSet(opts.ProductAllowedChannelIDs)
	modelSet := stringSet(opts.ProductAllowedModels)
	checkProviders := len(providerSet) > 0
	checkChannels := len(channelSet) > 0
	checkModels := len(modelSet) > 0

	providerOrChannelOK := !checkProviders && !checkChannels
	modelOK := !checkModels
	for _, channel := range candidates {
		if checkProviders {
			if _, ok := providerSet[channel.ProviderID]; ok {
				providerOrChannelOK = true
			}
		}
		if checkChannels {
			if _, ok := channelSet[channel.ChannelID]; ok {
				providerOrChannelOK = true
			}
		}
		if checkModels && channelAllowsAnyProductModel(channel, modelSet) {
			modelOK = true
		}
	}
	if !providerOrChannelOK {
		return fmt.Errorf("%s provider intersection empty", label)
	}
	if !modelOK {
		return fmt.Errorf("%s model intersection empty", label)
	}
	return nil
}

func targetChannels(targetType, targetID string, groupsByID map[string]types.ProductRouteGroup, channelsByID map[string]types.ProductRouteChannel) []types.ProductRouteChannel {
	switch targetType {
	case "PRODUCT_CHANNEL":
		if channel, ok := channelsByID[targetID]; ok {
			return []types.ProductRouteChannel{channel}
		}
	case "GROUP":
		group, ok := groupsByID[targetID]
		if !ok {
			return nil
		}
		out := make([]types.ProductRouteChannel, 0, len(group.Members))
		for _, member := range group.Members {
			if member.Role == "shadow" {
				continue
			}
			if channel, ok := channelsByID[member.ProductChannelID]; ok {
				out = append(out, channel)
			}
		}
		return out
	}
	return nil
}

func channelAllowsAnyProductModel(channel types.ProductRouteChannel, productModels map[string]struct{}) bool {
	denied := stringSet(channel.DeniedModels)
	for _, model := range channel.AllowedModels {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, ok := productModels[model]; !ok {
			continue
		}
		if _, blocked := denied[model]; blocked {
			continue
		}
		return true
	}
	return false
}

func allowedValue(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func stringSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out[value] = struct{}{}
		}
	}
	return out
}

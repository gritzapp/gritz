package apiserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/icholy/gritz/internal/auth/apiauth"
	"github.com/icholy/gritz/internal/auth/authscope"
	"github.com/icholy/gritz/internal/model"
	gritzv1 "github.com/icholy/gritz/internal/proto/gritz/v1"
	"github.com/icholy/gritz/internal/server/eventrouter"
)

func (s *Server) CreateOrg(ctx context.Context, req *gritzv1.CreateOrgRequest) (*gritzv1.CreateOrgResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpOrgCreate) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot create org"))
	}
	if req.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name is required"))
	}
	org := &model.Org{
		Name:  req.Name,
		Owner: caller.ID,
	}
	err := s.store.WithTx(ctx, nil, func(tx *sql.Tx) error {
		if err := s.store.CreateOrg(ctx, tx, org); err != nil {
			return err
		}
		if err := s.store.AddOrgMember(ctx, tx, &model.OrgMember{
			OrgID:  org.ID,
			UserID: caller.ID,
			Role:   "owner",
		}); err != nil {
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.log.InfoContext(ctx, "org created", "id", org.ID, "name", org.Name, "owner", caller.ID)
	return &gritzv1.CreateOrgResponse{Org: org.Proto()}, nil
}

func (s *Server) ListOrgs(ctx context.Context, req *gritzv1.ListOrgsRequest) (*gritzv1.ListOrgsResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpOrgRead) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot list orgs"))
	}
	orgs, err := s.store.ListOrgsByMember(ctx, nil, caller.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return &gritzv1.ListOrgsResponse{Orgs: model.ProtoMap(orgs)}, nil
}

func (s *Server) DeleteOrg(ctx context.Context, req *gritzv1.DeleteOrgRequest) (*gritzv1.DeleteOrgResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpOrgDelete) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot delete org"))
	}
	org, err := s.store.GetOrg(ctx, nil, req.Id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("org not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if org.Owner != caller.ID {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("only the org owner can delete it"))
	}
	user, err := s.store.GetUser(ctx, nil, caller.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if user.DefaultOrgID == req.Id {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("cannot delete your default org"))
	}
	if err := s.store.ArchiveOrg(ctx, nil, req.Id); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.log.InfoContext(ctx, "org archived", "id", req.Id, "owner", caller.ID)
	return &gritzv1.DeleteOrgResponse{}, nil
}

func (s *Server) AddOrgMember(ctx context.Context, req *gritzv1.AddOrgMemberRequest) (*gritzv1.AddOrgMemberResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpOrgWrite) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot write org"))
	}
	org, err := s.store.GetOrg(ctx, nil, caller.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if org.Owner != caller.ID {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("only the org owner can add members"))
	}
	if req.Email == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("email is required"))
	}
	user, err := s.store.GetUserByEmail(ctx, nil, req.Email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no user found with email %q", req.Email))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	member := &model.OrgMember{
		OrgID:  caller.OrgID,
		UserID: user.ID,
		Role:   "member",
	}
	if err := s.store.AddOrgMember(ctx, nil, member); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.log.InfoContext(ctx, "org member added", "user_id", user.ID, "email", req.Email)
	s.publish(model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "added", Type: "org_members"}},
		OrgID:     caller.OrgID,
		UserID:    caller.ID,
		ClientID:  caller.ClientID,
		Time:      time.Now(),
	})
	return &gritzv1.AddOrgMemberResponse{
		Member: &gritzv1.OrgMember{
			OrgId:  member.OrgID,
			UserId: member.UserID,
			Email:  user.Email,
			Name:   user.Name,
			Role:   member.Role,
		},
	}, nil
}

func (s *Server) RemoveOrgMember(ctx context.Context, req *gritzv1.RemoveOrgMemberRequest) (*gritzv1.RemoveOrgMemberResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpOrgWrite) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot write org"))
	}
	org, err := s.store.GetOrg(ctx, nil, caller.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if org.Owner != caller.ID {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("only the org owner can remove members"))
	}
	if req.UserId == org.Owner {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("cannot remove the org owner"))
	}
	if err := s.store.RemoveOrgMember(ctx, nil, caller.OrgID, req.UserId); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.log.InfoContext(ctx, "org member removed", "user_id", req.UserId)
	s.publish(model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "removed", Type: "org_members"}},
		OrgID:     caller.OrgID,
		UserID:    caller.ID,
		ClientID:  caller.ClientID,
		Time:      time.Now(),
	})
	return &gritzv1.RemoveOrgMemberResponse{}, nil
}

func (s *Server) ListOrgMembers(ctx context.Context, req *gritzv1.ListOrgMembersRequest) (*gritzv1.ListOrgMembersResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpOrgRead) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot read org"))
	}
	members, err := s.store.ListOrgMembersWithUsers(ctx, nil, caller.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return &gritzv1.ListOrgMembersResponse{Members: model.ProtoMap(members)}, nil
}

func (s *Server) GetOrgSettings(ctx context.Context, req *gritzv1.GetOrgSettingsRequest) (*gritzv1.GetOrgSettingsResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpOrgRead) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot read org"))
	}
	org, err := s.store.GetOrg(ctx, nil, caller.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &gritzv1.GetOrgSettingsResponse{
		McpUrl:               s.baseURL + "/mcp",
		GithubInstallationId: org.GitHubInstallationID,
	}
	if s.atlassian != nil {
		resp.AtlassianWebhookSecret = org.AtlassianWebhookSecret
		resp.AtlassianWebhookUrl = s.atlassian.WebhookURL(caller.OrgID)
	}
	if s.github != nil {
		resp.GithubAppUrl = s.github.AppInstallURL()
	}
	return resp, nil
}

func (s *Server) GenerateAtlassianWebhookSecret(ctx context.Context, req *gritzv1.GenerateAtlassianWebhookSecretRequest) (*gritzv1.GenerateAtlassianWebhookSecretResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpOrgWrite) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot write org"))
	}
	secret, err := s.atlassian.GenerateWebhookSecret(ctx, caller.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return &gritzv1.GenerateAtlassianWebhookSecretResponse{
		Secret:     secret,
		WebhookUrl: s.atlassian.WebhookURL(caller.OrgID),
	}, nil
}

func (s *Server) UnlinkGitHubAccount(ctx context.Context, req *gritzv1.UnlinkGitHubAccountRequest) (*gritzv1.UnlinkGitHubAccountResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpAccountWrite) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot write account"))
	}
	if err := s.store.UnlinkGitHubAccount(ctx, nil, caller.ID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.log.InfoContext(ctx, "github account unlinked", "owner", caller.ID)
	return &gritzv1.UnlinkGitHubAccountResponse{}, nil
}

func (s *Server) UnlinkAtlassianAccount(ctx context.Context, req *gritzv1.UnlinkAtlassianAccountRequest) (*gritzv1.UnlinkAtlassianAccountResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpAccountWrite) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot write account"))
	}
	if err := s.atlassian.UnlinkAccount(ctx, caller.ID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.log.InfoContext(ctx, "atlassian account unlinked", "owner", caller.ID)
	return &gritzv1.UnlinkAtlassianAccountResponse{}, nil
}

func (s *Server) GetRoutingRules(ctx context.Context, req *gritzv1.GetRoutingRulesRequest) (*gritzv1.GetRoutingRulesResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpOrgRead) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot read org"))
	}
	rules, err := s.store.GetOrgRoutingRules(ctx, nil, caller.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	pb := make([]*gritzv1.RoutingRule, len(rules))
	for i := range rules {
		pb[i] = rules[i].Proto()
	}
	return &gritzv1.GetRoutingRulesResponse{Rules: pb}, nil
}

func (s *Server) SetRoutingRules(ctx context.Context, req *gritzv1.SetRoutingRulesRequest) (*gritzv1.SetRoutingRulesResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpOrgWrite) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot write org"))
	}
	rules := make([]model.RoutingRule, len(req.Rules))
	for i, r := range req.Rules {
		rule := model.RoutingRuleFromProto(r)
		if err := eventrouter.DefaultSchemaRegistry.Validate(rule); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		rules[i] = rule
	}
	if err := s.store.SetOrgRoutingRules(ctx, nil, caller.OrgID, rules); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	pb := make([]*gritzv1.RoutingRule, len(rules))
	for i := range rules {
		pb[i] = rules[i].Proto()
	}
	return &gritzv1.SetRoutingRulesResponse{Rules: pb}, nil
}

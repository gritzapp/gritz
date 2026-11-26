package eventrouter

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/server/pubsub"
	"github.com/gritzapp/gritz/internal/store"
)

// Attrs maps a dimension name to the event's values for that dimension.
// Single-valued dimensions are one-element slices. Webhook handlers populate it
// at extraction time and the attribute matcher (Match) reads from it. See
// docs/src/content/docs/proposals/implemented/attribute-based-event-matching.md.
type Attrs map[string][]string

// InputEvent represents a parsed webhook event ready for routing. Its matchable
// dimensions travel in Attrs (populated by the webhook handlers), which the
// attribute matcher reads via Attr.
type InputEvent struct {
	Source      string
	Type        string
	Description string
	Data        string
	URL         string
	UserID      string
	// Orgs names the orgs this event belongs to, resolved by the webhook handler
	// independent of the actor's membership (GitHub: the orgs sharing the App
	// installation; Atlassian: the org in the webhook's ?org= param; custom: the
	// org it was posted to). It gates non-member routing: a public trigger or
	// link kind admits the event in one of these orgs even when UserID is empty
	// or the actor is not a member.
	Orgs []int64
	// Attrs carries the event's matchable dimensions keyed by dimension name
	// (e.g. "mention", "assignee", "label"), which the attribute matcher reads.
	Attrs Attrs
	// Details is source-defined key/value context copied verbatim into the
	// persisted ExternalPayload for consumers (agent, UI). It is distinct from
	// Attrs: Attrs are matchable routing dimensions the matcher reads and the
	// router drops after routing; Details is persisted payload the router does
	// not interpret.
	Details map[string]string
	// Meta carries source-specific data that the router does not interpret. It
	// lets webhook handlers attach native identity (e.g. the GitHub author)
	// without leaking source-specific types into eventrouter.
	Meta any
}

// Attr returns the event's values for a dimension. The "body" and "url"
// dimensions are derived views over Data and URL so extractors don't
// duplicate them; any other key reads from Attrs (nil/absent -> nil).
func (e InputEvent) Attr(key string) []string {
	switch key {
	case "body":
		return []string{e.Data}
	case "url":
		return []string{e.URL}
	default:
		return e.Attrs[key]
	}
}

// Delivery is a task an event is delivered to through one of its subscribed
// links, and the link kind that matched.
type Delivery struct {
	OrgID   int64
	AgentID int64 // the task's agent
	TaskID  int64
	Kind    string
	// EventID is the external event Apply added to the task; zero in a Plan,
	// and when Apply failed to add it.
	EventID int64
}

// TriggerMatch is an agent whose trigger matched an event, and whether it
// creates a task or yields.
type TriggerMatch struct {
	OrgID        int64
	Agent        *model.Agent
	TriggerIndex int
	// YieldedTo are the unarchived tasks holding a claim the trigger yields to
	// on the event's resource. The trigger creates no task when any do.
	YieldedTo []int64
	// TaskID is the task Apply created and EventID the external event it added
	// to it; zero in a Plan, for a trigger that yielded, and when Apply failed
	// to create it.
	TaskID  int64
	EventID int64
}

// Trigger returns the trigger that matched.
func (m TriggerMatch) Trigger() model.AgentTrigger {
	return m.Agent.Triggers[m.TriggerIndex]
}

// Plan is the side-effect-free result of routing an event: the tasks it is
// delivered to, and the agents whose trigger matched it. Deliveries and
// triggers are independent: an event can be delivered to a task and create
// another through a trigger.
type Plan struct {
	Deliveries []Delivery
	Triggers   []TriggerMatch
}

// RouteOutcome is what Apply did with an event in one org: its planned
// deliveries and trigger matches, with the IDs of the tasks and events it
// wrote set. It is what Route hands the OnRouteOutcome callback.
type RouteOutcome struct {
	Input      InputEvent // the routed event, including its Meta
	OrgID      int64
	Deliveries []Delivery
	Triggers   []TriggerMatch
}

// Created reports whether a trigger created a task.
func (o RouteOutcome) Created() bool {
	return slices.ContainsFunc(o.Triggers, func(m TriggerMatch) bool { return m.TaskID != 0 })
}

// Delivered reports whether the event was added to a subscribed task.
func (o RouteOutcome) Delivered() bool {
	return slices.ContainsFunc(o.Deliveries, func(d Delivery) bool { return d.EventID != 0 })
}

// Router routes events to the agents of the orgs they belong to: it delivers
// them to subscribed tasks and creates tasks from triggers.
type Router struct {
	Log       *slog.Logger
	Store     *store.Store
	Publisher pubsub.Publisher
	// Registry holds the built-in event and link types; an org's custom event
	// types are read from the store.
	Registry *SchemaRegistry

	// OnRouteOutcome, if set, is called synchronously once per org with a
	// delivery or a trigger match, after Apply handles it, with the request
	// context. The Router imposes no concurrency or lifetime policy — the
	// callback owns that (e.g. spawning its own goroutine). Optional — nil
	// disables it (e.g. the Atlassian router leaves it unset).
	OnRouteOutcome func(ctx context.Context, outcome RouteOutcome)
}

// Plan routes the event without side effects, in every org it belongs to (see
// store.ListEventOrgs):
//
//   - Delivery: the subscribed links on the event's resource — the canonical
//     URL its event type's link types give it — whose kind, on the task's
//     agent, declares the event's type, and admits the actor: a member, or
//     anyone for a public kind. Each task gets the event once, through its
//     first matching link.
//   - Triggers: each agent's first trigger that matches the event and admits
//     the actor (a public trigger, for a non-member). A trigger with YieldTo
//     yields while an unarchived task holds one of those claims on the
//     resource.
//
// Scoping the result to a single org (e.g. the test path's caller.OrgID) is
// the caller's job, done by filtering the returned plan.
//
// Plan reads one snapshot, so every subscribed link it finds belongs to a task
// of an agent it loaded.
func (r *Router) Plan(ctx context.Context, input InputEvent) (*Plan, error) {
	var plan *Plan
	err := r.Store.WithSnapshot(ctx, func(tx *sql.Tx) error {
		var err error
		plan, err = r.plan(ctx, tx, input)
		return err
	})
	return plan, err
}

func (r *Router) plan(ctx context.Context, tx *sql.Tx, input InputEvent) (*Plan, error) {
	orgs, err := r.Store.ListEventOrgs(ctx, tx, input.UserID, input.Orgs)
	if err != nil {
		return nil, err
	}
	plan := &Plan{}
	if len(orgs) == 0 {
		return plan, nil
	}
	var orgIDs []int64
	member := map[int64]bool{}
	for _, org := range orgs {
		orgIDs = append(orgIDs, org.OrgID)
		member[org.OrgID] = org.IsMember
	}
	agents, err := r.Store.ListAgentsForOrgs(ctx, tx, orgIDs)
	if err != nil {
		return nil, err
	}
	agentByID := map[int64]*model.Agent{}
	for _, agent := range agents {
		agentByID[agent.ID] = agent
	}
	defs, err := r.eventTypes(ctx, tx, input, orgIDs)
	if err != nil {
		return nil, err
	}

	// The event's resource per org: an org's custom event type can differ from
	// another's. Orgs sharing a resource share a link lookup.
	var urls []string
	orgsByURL := map[string][]int64{}
	for _, orgID := range orgIDs {
		def, ok := defs[orgID]
		if !ok {
			continue
		}
		url, ok := r.Registry.CanonicalURL(def, input.URL)
		if !ok {
			continue
		}
		if _, seen := orgsByURL[url]; !seen {
			urls = append(urls, url)
		}
		orgsByURL[url] = append(orgsByURL[url], orgID)
	}

	// Every subscribed link on the resource, by org. They hold the claims
	// triggers yield to, whether or not they deliver this event.
	linksByOrg := map[int64][]store.SubscribedLink{}
	for _, url := range urls {
		links, err := r.Store.FindSubscribedLinks(ctx, tx, url, orgsByURL[url])
		if err != nil {
			return nil, err
		}
		delivered := map[int64]bool{}
		for _, link := range links {
			linksByOrg[link.OrgID] = append(linksByOrg[link.OrgID], link)
			if delivered[link.Link.TaskID] {
				continue
			}
			// Unarchiving a task of a deleted agent fails, so every unarchived
			// task's agent is live.
			agent, ok := agentByID[link.AgentID]
			if !ok {
				panic(fmt.Sprintf("eventrouter: unarchived task %d belongs to agent %d, which isn't a live agent of org %d", link.Link.TaskID, link.AgentID, link.OrgID))
			}
			// The kind is gone when its agent dropped it since.
			kind, ok := agent.Link(link.Link.Kind)
			if !ok {
				continue
			}
			if !slices.Contains(defs[link.OrgID].LinkTypes, kind.Type) {
				continue
			}
			if len(kind.Events) > 0 && !slices.Contains(kind.Events, input.Type) {
				continue
			}
			if !member[link.OrgID] && !kind.Public {
				continue
			}
			delivered[link.Link.TaskID] = true
			plan.Deliveries = append(plan.Deliveries, Delivery{
				OrgID:   link.OrgID,
				AgentID: agent.ID,
				TaskID:  link.Link.TaskID,
				Kind:    link.Link.Kind,
			})
		}
	}

	for _, agent := range agents {
		for i, trigger := range agent.Triggers {
			if !member[agent.OrgID] && !trigger.Public {
				continue
			}
			if !Match(trigger, input) {
				continue
			}
			match := TriggerMatch{OrgID: agent.OrgID, Agent: agent, TriggerIndex: i}
			if len(trigger.YieldTo) > 0 {
				match.YieldedTo = claimHolders(linksByOrg[agent.OrgID], trigger.YieldTo)
			}
			plan.Triggers = append(plan.Triggers, match)
			break
		}
	}
	return plan, nil
}

// claimHolders returns the distinct tasks, in ascending order, of the links
// whose claim is one of claims.
func claimHolders(links []store.SubscribedLink, claims []string) []int64 {
	var holders []int64
	for _, link := range links {
		if slices.Contains(claims, link.Link.Claim) {
			holders = append(holders, link.Link.TaskID)
		}
	}
	slices.Sort(holders)
	return slices.Compact(holders)
}

// eventTypes returns the event's type in each of orgIDs: the registry's for a
// built-in source, and each org's own for a custom one. An org with no such
// type is left out.
func (r *Router) eventTypes(ctx context.Context, tx *sql.Tx, input InputEvent, orgIDs []int64) (map[int64]model.EventTypeDef, error) {
	defs := map[int64]model.EventTypeDef{}
	if input.Source != SourceCustom {
		if def, ok := r.Registry.EventTypeFor(input.Source, input.Type); ok {
			for _, orgID := range orgIDs {
				defs[orgID] = def
			}
		}
		return defs, nil
	}
	for _, orgID := range orgIDs {
		custom, err := r.Store.GetOrgCustomEventTypes(ctx, tx, orgID)
		if err != nil {
			return nil, err
		}
		if i := slices.IndexFunc(custom, func(def model.EventTypeDef) bool { return def.Type == input.Type }); i >= 0 {
			defs[orgID] = custom[i]
		}
	}
	return defs, nil
}

// Apply is the write half of routing: it adds the event to every planned
// delivery's task, waking it, and creates a task for every trigger match that
// didn't yield. A task it fails to wake or create is logged and left without
// IDs, so one bad task doesn't stop the rest. It returns one RouteOutcome per
// org of the plan, in the plan's order. It never invokes OnRouteOutcome; Route
// layers that on top of the returned outcomes, and TestEvent's fire mode calls
// Apply directly to route a synthetic event as an ordinary event without any
// outbound side effects.
func (r *Router) Apply(ctx context.Context, input InputEvent, plan *Plan) []RouteOutcome {
	var outcomes []RouteOutcome
	index := map[int64]int{}
	outcome := func(orgID int64) *RouteOutcome {
		i, ok := index[orgID]
		if !ok {
			i = len(outcomes)
			index[orgID] = i
			outcomes = append(outcomes, RouteOutcome{Input: input, OrgID: orgID})
		}
		return &outcomes[i]
	}
	for _, d := range plan.Deliveries {
		event, err := r.attach(ctx, d.TaskID, input, d.OrgID)
		if err != nil {
			r.Log.Error("failed to deliver event to task", "task_id", d.TaskID, "err", err)
		} else {
			d.EventID = event.ID
		}
		o := outcome(d.OrgID)
		o.Deliveries = append(o.Deliveries, d)
	}
	for _, m := range plan.Triggers {
		if len(m.YieldedTo) == 0 {
			task, event, err := r.create(ctx, input, m)
			if err != nil {
				r.Log.Error("failed to create task from trigger", "org_id", m.OrgID, "agent_id", m.Agent.ID, "err", err)
			} else {
				m.TaskID = task.ID
				m.EventID = event.ID
			}
		}
		o := outcome(m.OrgID)
		o.Triggers = append(o.Triggers, m)
	}
	return outcomes
}

// Route plans the event and applies the plan (see Plan and Apply), then hands
// each org's outcome to OnRouteOutcome. Returns the number of tasks woken or
// created.
func (r *Router) Route(ctx context.Context, input InputEvent) (int, error) {
	// The URL names the event's resource, which subscribed links and claims
	// are keyed on.
	if input.URL == "" {
		return 0, nil
	}
	plan, err := r.Plan(ctx, input)
	if err != nil {
		return 0, err
	}
	var n int
	for _, outcome := range r.Apply(ctx, input, plan) {
		for _, d := range outcome.Deliveries {
			if d.EventID != 0 {
				n++
			}
		}
		for _, m := range outcome.Triggers {
			if m.TaskID != 0 {
				n++
			}
		}
		if r.OnRouteOutcome != nil {
			r.OnRouteOutcome(ctx, outcome)
		}
	}
	return n, nil
}

func (r *Router) publish(ctx context.Context, n model.Notification) {
	if n.Ignore {
		return
	}
	if r.Publisher == nil {
		return
	}
	if err := r.Publisher.Publish(ctx, n); err != nil {
		r.Log.Warn("failed to publish notification", "err", err)
	}
}

// attach adds an external event to a task, starts the task, and publishes a
// change notification. It sets the wake ChannelMessage only when the attach
// restarts a task that had finished its run (IsDone); an empty ChannelMessage
// keeps the agent channel silent for already-running / already-queued tasks
// while the FE still receives the same notification.
//
// It returns the external event row it created so callers that report the
// written rows (TestEvent's fire mode) can surface it.
func (r *Router) attach(ctx context.Context, taskID int64, input InputEvent, orgID int64) (*model.Event, error) {
	event := &model.Event{
		TaskID: taskID,
		OrgID:  orgID,
		Wake:   true,
		Payload: &model.ExternalPayload{
			Description: input.Description,
			URL:         input.URL,
			Data:        input.Data,
			Details:     input.Details,
			Source:      input.Source,
			EventType:   input.Type,
		},
	}
	notification := model.Notification{
		Type:  "change",
		OrgID: orgID,
		Time:  time.Now(),
	}
	err := r.Store.WithTx(ctx, nil, func(tx *sql.Tx) error {
		if err := r.Store.CreateEvent(ctx, tx, event); err != nil {
			return err
		}
		task, err := r.Store.GetTaskForUpdate(ctx, tx, taskID, event.OrgID)
		if err != nil {
			return err
		}
		wasDone := task.IsDone()
		from := task.Clone()
		task.Start()
		if err := r.Store.UpdateTask(ctx, tx, task); err != nil {
			return err
		}
		if ev, ok := task.CommandEvent(from); ok {
			if err := r.Store.CreateEvent(ctx, tx, ev); err != nil {
				return err
			}
		}
		// The external event above already records the trigger that restarted the
		// task, so a router-emitted RESTARTED lifecycle event would be redundant:
		// the timeline already shows why the task woke. Restarted lifecycle events
		// are reserved for user-initiated restarts (see RestartTask), where there's
		// no external event to explain the transition.
		notification.Resources = []model.NotificationResource{
			{Action: "updated", Type: "task", ID: task.ID},
			{Action: "updated", Type: "event", ID: event.ID},
			{Action: "appended", Type: "task_events", ID: task.ID},
		}
		notification.Runner = task.PendingRunner()
		if wasDone {
			notification.ChannelMessage = fmt.Sprintf("Task %d woken by event %d: %s (%s)", task.ID, event.ID, input.Description, input.URL)
		}
		return tx.Commit()
	})
	if err != nil {
		return nil, err
	}
	r.publish(ctx, notification)
	return event, nil
}

// create creates a task of the matched trigger's agent from the agent's
// template, in a single transaction: the task, the event as its first event,
// the trigger's link and the agent's prompt as its first instruction. The task
// gets the trigger's link only when the link kind's type accepts the event's
// URL.
//
// It returns the created task and the external (trigger) event row — the one
// carrying the ExternalPayload — so callers that report the written rows
// (TestEvent's fire mode) can surface them.
func (r *Router) create(ctx context.Context, input InputEvent, match TriggerMatch) (*model.Task, *model.Event, error) {
	agent := match.Agent
	trigger := match.Trigger()
	var link *model.Link
	if trigger.Link != "" {
		kind, ok := agent.Link(trigger.Link)
		if !ok {
			panic(fmt.Sprintf("eventrouter: agent %d trigger %d names unknown link kind %q", agent.ID, match.TriggerIndex, trigger.Link))
		}
		lt, ok := r.Registry.LinkType(kind.Type)
		if !ok {
			panic(fmt.Sprintf("eventrouter: agent %d link kind %q names unregistered link type %q", agent.ID, trigger.Link, kind.Type))
		}
		if url, ok := lt.Canonical(input.URL); ok {
			// Leave the title empty: the triggering event's description lives
			// on the external event.
			link = &model.Link{
				URL:       url,
				Relevance: "trigger",
				Kind:      kind.Name(),
				Claim:     kind.Claim,
				CreatedAt: time.Now().UTC(),
			}
		}
	}
	var notification model.Notification
	var task *model.Task
	var external *model.Event
	err := r.Store.WithTx(ctx, nil, func(tx *sql.Tx) error {
		prompt := agent.Prompt
		if prompt == "" {
			prompt = fmt.Sprintf("You were created by a trigger of agent %q in response to a %s %s event.", agent.Name, input.Source, input.Type)
		}
		// Hold the agent so a concurrent DeleteAgent sees this task; one
		// deleted since the plan creates nothing.
		if err := r.Store.LockLiveAgent(ctx, tx, match.OrgID, agent.ID); err != nil {
			return fmt.Errorf("lock agent %d: %w", agent.ID, err)
		}
		task = &model.Task{
			Runner:      agent.Runner,
			Workspace:   agent.Workspace,
			Status:      model.TaskStatusPending,
			Command:     model.TaskCommandStart,
			Version:     1,
			OrgID:       match.OrgID,
			AgentID:     agent.ID,
			AutoArchive: agent.AutoArchive,
			// The task gets the agent's variables as saved; the runner resolves
			// them when it starts it.
			Variables: agent.Variables,
		}
		if err := r.Store.CreateTask(ctx, tx, task); err != nil {
			return err
		}
		// Emit the external (trigger) event first so it leads the timeline (ordered
		// by event id), the same way it appears when an event wakes an existing task
		// (see attach). The event that caused the task to exist should precede the
		// "Created" lifecycle event it triggered.
		external = &model.Event{
			TaskID: task.ID,
			OrgID:  task.OrgID,
			Payload: &model.ExternalPayload{
				Description: input.Description,
				URL:         input.URL,
				Data:        input.Data,
				Details:     input.Details,
				Source:      input.Source,
				EventType:   input.Type,
			},
		}
		if err := r.Store.CreateEvent(ctx, tx, external); err != nil {
			return err
		}
		// Record the creation as a lifecycle event. The router (not a user) created
		// the task, so the actor is the router actor; a freshly created task has no
		// prior status. Emit it after the external trigger but before the link and
		// instruction events so the timeline reads
		// External -> Created -> Link -> Instruction.
		if err := r.Store.CreateEvent(ctx, tx, &model.Event{
			TaskID: task.ID,
			OrgID:  task.OrgID,
			Payload: &model.LifecyclePayload{
				Kind:     model.LifecycleKindCreated,
				Actor:    model.RouterActor,
				ToStatus: task.Status.Label(),
			},
		}); err != nil {
			return err
		}
		if ev, ok := task.CommandEvent(nil); ok {
			if err := r.Store.CreateEvent(ctx, tx, ev); err != nil {
				return err
			}
		}
		// task_links is the subscription/list projection; the link event is the
		// timeline source of truth. Write both in the same tx so they can't drift
		// (mirrors the apiserver CreateLink path).
		if link != nil {
			link.TaskID = task.ID
			if err := r.Store.CreateLink(ctx, tx, link); err != nil {
				return err
			}
			if err := r.Store.CreateEvent(ctx, tx, &model.Event{
				TaskID:  task.ID,
				OrgID:   task.OrgID,
				Payload: link.EventPayload(),
			}); err != nil {
				return err
			}
		}
		// Instruction events always wake; the task already starts via
		// Command=Start.
		if err := r.Store.CreateEvent(ctx, tx, &model.Event{
			TaskID:  task.ID,
			OrgID:   task.OrgID,
			Wake:    true,
			Payload: &model.InstructionPayload{Text: prompt},
		}); err != nil {
			return err
		}
		notification = model.Notification{
			Type:  "change",
			OrgID: task.OrgID,
			Time:  time.Now(),
			Resources: []model.NotificationResource{
				{Action: "created", Type: "task", ID: task.ID},
				{Action: "appended", Type: "task_events", ID: task.ID},
			},
			Runner:         task.PendingRunner(),
			ChannelMessage: fmt.Sprintf("Task %d created by agent %q for event: %s (%s)", task.ID, agent.Name, input.Description, input.URL),
		}
		return tx.Commit()
	})
	if err != nil {
		return nil, nil, err
	}
	r.publish(ctx, notification)
	return task, external, nil
}

package homelab

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
)

// Group is a group, and the actual point, its gid. Ported from src/resources/group.ts.
//
// Filesystem permissions record numbers, not names, so a rebuild where groupadd hands out the next
// free number leaves every file owned by a group that does not exist. The gid is declarable, read
// back, and a mismatch is drift. Renumbering an existing group is refused unless renumber is set.
type Group struct{}

// GroupArgs are the inputs of a Group.
type GroupArgs struct {
	Name     string `pulumi:"name"`
	Gid      *int   `pulumi:"gid,optional"`
	Renumber *bool  `pulumi:"renumber,optional"`
}

// GroupState is what is kept in state. It holds nothing about the route to the machine.
type GroupState struct {
	Name     string   `pulumi:"name"`
	Gid      int      `pulumi:"gid"`
	Renumber bool     `pulumi:"renumber"`
	Members  []string `pulumi:"members"`
}

func (g *GroupArgs) Annotate(a infer.Annotator) {
	a.Describe(&g.Name, "The group's name.")
	a.Describe(&g.Gid, "The numeric id, which is what the filesystem actually stores.")
	a.Describe(&g.Renumber, "Permission to change the gid of a group that already exists. Renumbering does not chown anything.")
}

func (g *GroupState) Annotate(a infer.Annotator) {
	a.Describe(&g.Members, "Who is in it, as getent reports them. Read, never managed.")
}

func (g *Group) Annotate(a infer.Annotator) {
	a.Describe(&g, "A group whose number is the part that matters.")
	a.SetToken("index", "Group")
}

// GroupEntry is one parsed getent group line.
type GroupEntry struct {
	Name    string
	Gid     int
	Members []string
}

// parseGroupEntry reads `storage:x:1002:admin,player`, or returns nil for a line that is not a group.
func parseGroupEntry(line string) *GroupEntry {
	fields := strings.Split(jscompat.Trim(line), ":")
	if len(fields) < 3 {
		return nil
	}
	gid, ok := jsNumber(fields[2])
	if !ok {
		return nil
	}
	members := []string{}
	if len(fields) > 3 {
		for _, m := range strings.Split(fields[3], ",") {
			if m != "" {
				members = append(members, m)
			}
		}
	}
	return &GroupEntry{Name: fields[0], Gid: gid, Members: members}
}

// jsNumber is Number(text) followed by Number.isFinite, restricted to the integers a gid can be.
func jsNumber(text string) (int, bool) {
	f := jscompat.Number(text)
	if math.IsNaN(f) || f > 1e15 || f < -1e15 {
		return 0, false
	}
	return int(f), true
}

// renumberRefusal says why a gid change is refused, or "" when there is nothing to refuse.
func renumberRefusal(name string, gid *int, renumber bool, existing *int) string {
	if existing == nil || gid == nil || *gid == *existing {
		return ""
	}
	if renumber {
		return ""
	}
	return fmt.Sprintf("%s exists with gid %d and the code says %d. ", name, *existing, *gid) +
		fmt.Sprintf("Renumbering does not chown anything, so every file owned by %d would be orphaned by a ", *existing) +
		"command that reports success. Set renumber: true if you mean it, and afterwards run " +
		fmt.Sprintf("`find / -xdev -gid %d -exec chgrp %d {} +` on every filesystem that matters.", *existing, *gid)
}

func readGroup(ctx context.Context, m *machine, name string) (*GroupEntry, error) {
	asked, err := m.ask(ctx, "getent group "+shellQuote(name))
	if err != nil {
		return nil, err
	}
	if asked.code != 0 {
		return nil, nil
	}
	return parseGroupEntry(asked.out), nil
}

func machineFrom(ctx context.Context) (*machine, error) {
	cfg := infer.GetConfig[*Config](ctx)
	if cfg == nil || cfg.machine == nil {
		return nil, errors.New("the provider is not configured")
	}
	return cfg.machine, nil
}

func settle(ctx context.Context, m *machine, args GroupArgs) (GroupState, error) {
	existing, err := readGroup(ctx, m, args.Name)
	if err != nil {
		return GroupState{}, err
	}
	if existing == nil {
		gid := ""
		if args.Gid != nil {
			gid = fmt.Sprintf("-g %d ", *args.Gid)
		}
		if _, err := m.must(ctx, m.escalate("groupadd "+gid+shellQuote(args.Name))); err != nil {
			return GroupState{}, err
		}
	} else if args.Gid != nil && *args.Gid != existing.Gid {
		if refusal := renumberRefusal(args.Name, args.Gid, deref(args.Renumber), &existing.Gid); refusal != "" {
			return GroupState{}, errors.New(refusal)
		}
		if _, err := m.must(ctx, m.escalate(fmt.Sprintf("groupmod -g %d %s", *args.Gid, shellQuote(args.Name)))); err != nil {
			return GroupState{}, err
		}
	}
	actual, err := readGroup(ctx, m, args.Name)
	if err != nil {
		return GroupState{}, err
	}
	if actual == nil {
		return GroupState{}, fmt.Errorf("created %s but getent cannot find it", args.Name)
	}
	return GroupState{Name: actual.Name, Gid: actual.Gid, Renumber: deref(args.Renumber), Members: actual.Members}, nil
}

// Create adds the group, or adopts one that is already there.
func (Group) Create(ctx context.Context, req infer.CreateRequest[GroupArgs]) (infer.CreateResponse[GroupState], error) {
	if req.DryRun {
		return infer.CreateResponse[GroupState]{ID: req.Inputs.Name, Output: GroupState{
			Name: req.Inputs.Name, Gid: deref(req.Inputs.Gid), Renumber: deref(req.Inputs.Renumber), Members: []string{},
		}}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[GroupState]{}, err
	}
	state, err := settle(ctx, m, req.Inputs)
	if err != nil {
		return infer.CreateResponse[GroupState]{}, err
	}
	return infer.CreateResponse[GroupState]{ID: req.Inputs.Name, Output: state}, nil
}

// Read reports what the machine says; a group gone from the machine is forgotten, and the next up
// puts it back with its gid.
func (Group) Read(ctx context.Context, req infer.ReadRequest[GroupArgs, GroupState]) (infer.ReadResponse[GroupArgs, GroupState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[GroupArgs, GroupState]{}, err
	}
	actual, err := readGroup(ctx, m, req.ID)
	if err != nil {
		return infer.ReadResponse[GroupArgs, GroupState]{}, err
	}
	if actual == nil {
		return infer.ReadResponse[GroupArgs, GroupState]{}, nil
	}
	state := req.State
	state.Name, state.Gid, state.Members = actual.Name, actual.Gid, actual.Members
	return infer.ReadResponse[GroupArgs, GroupState]{ID: req.ID, Inputs: req.Inputs, State: state}, nil
}

// Update settles the group under its id; a name change is a replacement, never an update.
func (Group) Update(ctx context.Context, req infer.UpdateRequest[GroupArgs, GroupState]) (infer.UpdateResponse[GroupState], error) {
	args := req.Inputs
	args.Name = req.ID
	if req.DryRun {
		return infer.UpdateResponse[GroupState]{Output: GroupState{
			Name: args.Name, Gid: deref(args.Gid), Renumber: deref(args.Renumber), Members: req.State.Members,
		}}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[GroupState]{}, err
	}
	state, err := settle(ctx, m, args)
	if err != nil {
		return infer.UpdateResponse[GroupState]{}, err
	}
	return infer.UpdateResponse[GroupState]{Output: state}, nil
}

// Diff: a name change replaces (delete before replace); a declared gid that differs from the one
// read back is drift.
func (Group) Diff(_ context.Context, req infer.DiffRequest[GroupArgs, GroupState]) (p.DiffResponse, error) {
	nameChanged := req.State.Name != req.Inputs.Name
	gidChanged := req.Inputs.Gid != nil && req.State.Gid != *req.Inputs.Gid
	resp := p.DiffResponse{
		HasChanges:          nameChanged || gidChanged,
		DeleteBeforeReplace: true,
		DetailedDiff:        map[string]p.PropertyDiff{},
	}
	if nameChanged {
		resp.DetailedDiff["name"] = p.PropertyDiff{Kind: p.UpdateReplace}
	}
	if gidChanged {
		resp.DetailedDiff["gid"] = p.PropertyDiff{Kind: p.Update}
	}
	return resp, nil
}

// Delete does nothing. groupdel happily removes a group that merely owns a filesystem, and
// forgetting to describe a group is not a reason to orphan the files that reference it.
func (Group) Delete(context.Context, infer.DeleteRequest[GroupState]) (infer.DeleteResponse, error) {
	return infer.DeleteResponse{}, nil
}

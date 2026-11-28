package homelab

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/antimatter-studios/pulumi-homelab/provider/git"
	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
)

// GitCheckout is a repository cloned and checked out at a commit. Ported from src/resources/git.ts.
//
// A commit and nothing else: a branch moves by design and a tag moves whenever somebody
// force-pushes one, so neither can be read back and compared. It clones and checks out, and it
// does not build.
type GitCheckout struct{}

// GitCheckoutArgs are the inputs of a GitCheckout.
type GitCheckoutArgs struct {
	URL    string `pulumi:"url"`
	Path   string `pulumi:"path"`
	Commit string `pulumi:"commit"`
}

// GitCheckoutState is what is kept in state.
type GitCheckoutState struct {
	URL    string `pulumi:"url"`
	Path   string `pulumi:"path"`
	Commit string `pulumi:"commit"`
	Head   string `pulumi:"head"`
}

func (a *GitCheckoutArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.URL, "Where to clone from.")
	an.Describe(&a.Path, "Where the working tree goes. This is the resource's identity.")
	an.Describe(&a.Commit, "The commit to check out, as a full or abbreviated sha. A tag or a branch name is refused rather than resolved: get the sha with `git ls-remote <url> <tag>` and keep the tag beside it in a comment.")
}

func (s *GitCheckoutState) Annotate(an infer.Annotator) {
	an.Describe(&s.Head, "What the checkout is at now: the read, and where a hand `git checkout` shows up.")
}

func (r *GitCheckout) Annotate(an infer.Annotator) {
	an.Describe(&r, "A repository at a pinned commit, read back with `git rev-parse HEAD`. Deleting it is refused while it holds work that exists nowhere else.")
	an.SetToken("index", "GitCheckout")
}

var commitShaped = regexp.MustCompile(`(?i)^[0-9a-f]{7,40}$`)

// looksLikeCommit is whether a ref is written the way a commit is. Seven is git's own shortest
// default abbreviation.
func looksLikeCommit(ref string) bool {
	return commitShaped.MatchString(ref)
}

// commitRefusal says why a commit argument is refused, or "" when it is a sha.
func commitRefusal(url, commit string) string {
	if looksLikeCommit(commit) {
		return ""
	}
	return commit + " is not a commit. This resource pins a sha and nothing else, because a tag or a " +
		"branch can move and a moved ref cannot be told apart from drift. Run " +
		"`git ls-remote " + url + " " + commit + "` to get the sha, and keep " + commit + " beside it in a comment."
}

// sameCommit is whether an abbreviated declaration names the commit the checkout is at: prefix,
// case-insensitively, and only in that direction.
func sameCommit(head, commit string) bool {
	if head == "" || commit == "" {
		return false
	}
	return strings.HasPrefix(jscompat.ToLower(head), jscompat.ToLower(commit))
}

type checkoutActKind string

const (
	actClone    checkoutActKind = "clone"
	actCheckout checkoutActKind = "checkout"
	actStay     checkoutActKind = "nothing"
)

// checkoutAct is which of the three acts a checkout needs; head is nil when there is no checkout.
func checkoutAct(head *string, commit string) checkoutActKind {
	if head == nil {
		return actClone
	}
	if sameCommit(*head, commit) {
		return actStay
	}
	return actCheckout
}

// checkoutCommand puts a checkout at a commit, or ok false when it is already there.
func checkoutCommand(act checkoutActKind, url, path, commit string) (command string, ok bool) {
	if act == actStay {
		return "", false
	}
	at := "git -C " + shellQuote(path)
	// --detach because that is what a pinned checkout is
	checkout := at + " checkout --quiet --detach " + shellQuote(commit)
	if act == actClone {
		return `mkdir -p "$(dirname ` + shellQuote(path) + `)" && ` +
			"git clone --quiet " + shellQuote(url) + " " + shellQuote(path) + " && " + checkout, true
	}
	// --tags as well, because a commit reachable only from a tag created since the clone is not
	// fetched by default
	return at + " fetch --quiet --tags origin && " + checkout, true
}

// checkoutChanged is whether anything about this checkout needs doing.
func checkoutChanged(oldHead, oldCommit, oldURL, commit, url string) bool {
	return !sameCommit(oldHead, oldCommit) || oldCommit != commit || oldURL != url
}

// readCheckout is what the checkout is at, or nil when there is no checkout there.
func readCheckout(ctx context.Context, m *machine, path string) (*git.Checkout, error) {
	at := "git -C " + shellQuote(path)
	command := whenFilePresent(path+"/.git",
		at+" rev-parse HEAD && echo '"+git.HeadMarker+"' && { "+
			at+` remote get-url origin; status=$?; [ "$status" -eq 2 ] || exit "$status"; }`)
	// git remote get-url returns 2 when the remote does not exist.
	asked, err := m.commandResult(ctx, "git rev-parse HEAD / git remote get-url origin", m.escalate(command))
	if err != nil || asked.out == "" {
		return nil, err
	}
	c := git.ParseCheckout(asked.out)
	return &c, nil
}

func settleCheckout(ctx context.Context, m *machine, args GitCheckoutArgs) (GitCheckoutState, error) {
	if refusal := commitRefusal(args.URL, args.Commit); refusal != "" {
		return GitCheckoutState{}, errors.New(refusal)
	}
	before, err := readCheckout(ctx, m, args.Path)
	if err != nil {
		return GitCheckoutState{}, err
	}
	var head *string
	if before != nil {
		head = &before.Head
	}
	if command, ok := checkoutCommand(checkoutAct(head, args.Commit), args.URL, args.Path, args.Commit); ok {
		if _, err := m.must(ctx, m.escalate(command)); err != nil {
			return GitCheckoutState{}, err
		}
	}
	actual, err := readCheckout(ctx, m, args.Path)
	if err != nil {
		return GitCheckoutState{}, err
	}
	if actual == nil || !sameCommit(actual.Head, args.Commit) {
		at := "nothing"
		if actual != nil && actual.Head != "" {
			at = actual.Head
		}
		return GitCheckoutState{}, fmt.Errorf("checked out %s at %s on %s but HEAD is %s", args.Commit, args.Path, m.describe(), at)
	}
	return GitCheckoutState{URL: args.URL, Path: args.Path, Commit: args.Commit, Head: actual.Head}, nil
}

// Check refuses a commit that is not a sha: the argument is wrong rather than the machine, which is
// worth saying at preview, before anything runs.
func (GitCheckout) Check(ctx context.Context, req infer.CheckRequest) (infer.CheckResponse[GitCheckoutArgs], error) {
	args, failures, err := infer.DefaultCheck[GitCheckoutArgs](ctx, req.NewInputs)
	if err != nil || len(failures) > 0 {
		return infer.CheckResponse[GitCheckoutArgs]{Inputs: args, Failures: failures}, err
	}
	if c := req.NewInputs.Get("commit"); c.IsString() {
		url := ""
		if u := req.NewInputs.Get("url"); u.IsString() {
			url = u.AsString()
		}
		if refusal := commitRefusal(url, c.AsString()); refusal != "" {
			failures = append(failures, p.CheckFailure{Property: "commit", Reason: refusal})
		}
	}
	return infer.CheckResponse[GitCheckoutArgs]{Inputs: args, Failures: failures}, nil
}

// Create clones and checks out.
func (GitCheckout) Create(ctx context.Context, req infer.CreateRequest[GitCheckoutArgs]) (infer.CreateResponse[GitCheckoutState], error) {
	args := req.Inputs
	if req.DryRun {
		return infer.CreateResponse[GitCheckoutState]{ID: args.Path, Output: GitCheckoutState{URL: args.URL, Path: args.Path, Commit: args.Commit}}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[GitCheckoutState]{}, err
	}
	state, err := settleCheckout(ctx, m, args)
	if err != nil {
		return infer.CreateResponse[GitCheckoutState]{}, err
	}
	return infer.CreateResponse[GitCheckoutState]{ID: args.Path, Output: state}, nil
}

// Read: no checkout there any more and Pulumi forgets it, so the next up clones it back. The head
// always comes from the machine; an import takes the url and commit from it too.
func (GitCheckout) Read(ctx context.Context, req infer.ReadRequest[GitCheckoutArgs, GitCheckoutState]) (infer.ReadResponse[GitCheckoutArgs, GitCheckoutState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[GitCheckoutArgs, GitCheckoutState]{}, err
	}
	actual, err := readCheckout(ctx, m, req.ID)
	if err != nil || actual == nil {
		return infer.ReadResponse[GitCheckoutArgs, GitCheckoutState]{}, err
	}
	state := req.State
	if state.URL == "" {
		state.URL = req.Inputs.URL
		if state.URL == "" {
			state.URL = actual.URL
		}
	}
	if state.Commit == "" {
		state.Commit = req.Inputs.Commit
		if state.Commit == "" {
			state.Commit = actual.Head
		}
	}
	state.Path, state.Head = req.ID, actual.Head
	inputs := req.Inputs
	if inputs.Path == "" {
		inputs = GitCheckoutArgs{URL: state.URL, Path: req.ID, Commit: state.Commit}
	}
	return infer.ReadResponse[GitCheckoutArgs, GitCheckoutState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

// Update puts the checkout at the declared commit.
func (GitCheckout) Update(ctx context.Context, req infer.UpdateRequest[GitCheckoutArgs, GitCheckoutState]) (infer.UpdateResponse[GitCheckoutState], error) {
	args := req.Inputs
	args.Path = req.ID
	if req.DryRun {
		return infer.UpdateResponse[GitCheckoutState]{Output: GitCheckoutState{URL: args.URL, Path: args.Path, Commit: args.Commit, Head: req.State.Head}}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[GitCheckoutState]{}, err
	}
	state, err := settleCheckout(ctx, m, args)
	if err != nil {
		return infer.UpdateResponse[GitCheckoutState]{}, err
	}
	return infer.UpdateResponse[GitCheckoutState]{Output: state}, nil
}

// Diff: a moved head, commit or origin is a change; a different path or origin is a different
// checkout and replaces, deleting first.
func (GitCheckout) Diff(_ context.Context, req infer.DiffRequest[GitCheckoutArgs, GitCheckoutState]) (p.DiffResponse, error) {
	old, args := req.State, req.Inputs
	replace := old.Path != args.Path || old.URL != args.URL
	resp := p.DiffResponse{
		HasChanges:          replace || checkoutChanged(old.Head, old.Commit, old.URL, args.Commit, args.URL),
		DeleteBeforeReplace: true,
		DetailedDiff:        map[string]p.PropertyDiff{},
	}
	if old.Path != args.Path {
		resp.DetailedDiff["path"] = p.PropertyDiff{Kind: p.UpdateReplace}
	}
	if old.URL != args.URL {
		resp.DetailedDiff["url"] = p.PropertyDiff{Kind: p.UpdateReplace}
	}
	if old.Commit != args.Commit || !sameCommit(old.Head, old.Commit) {
		resp.DetailedDiff["commit"] = p.PropertyDiff{Kind: p.Update}
	}
	return resp, nil
}

// Delete removes the working tree after checking for local work.
func (GitCheckout) Delete(ctx context.Context, req infer.DeleteRequest[GitCheckoutState]) (infer.DeleteResponse, error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	path := req.State.Path
	if path == "" {
		path = req.ID
	}
	return infer.DeleteResponse{}, deleteCheckout(ctx, m, path)
}

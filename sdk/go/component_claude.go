package homelab

import (
	"fmt"
	"slices"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// SharedEntry names a directory to create or a file to link into each profile.
type SharedEntry struct {
	Name string
	// Kind is "directory" or "file".
	Kind string
	Mode *string
}

// SharedDefaults contains the work directories and settings file shared by default.
var SharedDefaults = []SharedEntry{
	{Name: "projects", Kind: "directory", Mode: new("0700")},
	{Name: "sessions", Kind: "directory", Mode: new("0700")},
	{Name: "session-env", Kind: "directory", Mode: new("0775")},
	{Name: "plugins", Kind: "directory", Mode: new("0775")},
	{Name: "settings.json", Kind: "file"},
}

// ClaudeSharedArgs describes the default profile that owns the shared files.
type ClaudeSharedArgs struct {
	Account string
	Path    string
	// Entries defaults to SharedDefaults when nil.
	Entries []SharedEntry
	Group   *string
	Mode    *string
}

// ClaudeShared holds the backend and the resources that profiles depend on.
type ClaudeShared struct {
	Path      string
	Entries   []SharedEntry
	Resources []pulumi.Resource
}

// ClaudeProfileArgs describes an additional Claude Code profile.
type ClaudeProfileArgs struct {
	Account string
	Shared  *ClaudeShared
	Dir     string
	Group   *string
	Mode    *string
}

// ClaudeProfile holds the new profile's links and CLAUDE_CONFIG_DIR.
type ClaudeProfile struct {
	Dir       string
	ConfigDir string
	Links     []*Symlink
}

// SharedTarget joins a backend directory and entry with a single separator between them.
func SharedTarget(shared, entry string) string {
	return strings.TrimRight(shared, "/") + "/" + entry
}

// ProfileRefusal returns an error if the profile would link its entries to themselves.
func ProfileRefusal(shared, dir string) error {
	dir = strings.TrimRight(dir, "/")
	if strings.TrimRight(shared, "/") == dir {
		return fmt.Errorf("claudeProfileResource: dir and shared are the same directory (%s); a profile has to be somewhere else", dir)
	}
	return nil
}

// ClaudeSharedResource declares the backend and its entry directories.
func ClaudeSharedResource(ctx *pulumi.Context, provider *Provider, name string, args *ClaudeSharedArgs, opts ...pulumi.ResourceOption) (*ClaudeShared, error) {
	if args == nil || provider == nil {
		return nil, fmt.Errorf("%s: ClaudeSharedResource requires arguments and a provider", name)
	}
	entries := args.Entries
	if entries == nil {
		entries = SharedDefaults
	}
	owner := pulumi.String(args.Account)
	group := pulumi.String(stringOr(args.Group, args.Account))
	opts = append(slices.Clone(opts), pulumi.Provider(provider))
	root, err := NewDirectory(ctx, name, &DirectoryArgs{
		Path: pulumi.String(args.Path), Owner: owner, Group: group, Mode: pulumi.String(stringOr(args.Mode, "0775")),
	}, opts...)
	if err != nil {
		return nil, err
	}

	resources := []pulumi.Resource{root}
	childOpts, err := claudeChildOptions(opts, root)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Kind != "directory" {
			continue
		}
		dir, err := NewDirectory(ctx, name+"-"+entry.Name, &DirectoryArgs{
			Path: pulumi.String(SharedTarget(args.Path, entry.Name)), Owner: owner, Group: group,
			Mode: pulumi.String(stringOr(entry.Mode, "0700")),
		}, childOpts...)
		if err != nil {
			return nil, err
		}
		resources = append(resources, dir)
	}
	return &ClaudeShared{Path: args.Path, Entries: entries, Resources: resources}, nil
}

// ClaudeProfileResource declares a profile directory and links it to the shared backend.
func ClaudeProfileResource(ctx *pulumi.Context, provider *Provider, name string, args *ClaudeProfileArgs, opts ...pulumi.ResourceOption) (*ClaudeProfile, error) {
	if args == nil || args.Shared == nil || provider == nil {
		return nil, fmt.Errorf("%s: ClaudeProfileResource requires arguments, a shared backend, and a provider", name)
	}
	if err := ProfileRefusal(args.Shared.Path, args.Dir); err != nil {
		return nil, err
	}

	opts = append(slices.Clone(opts), pulumi.Provider(provider))
	dir, err := NewDirectory(ctx, name, &DirectoryArgs{
		Path: pulumi.String(args.Dir), Owner: pulumi.String(args.Account),
		Group: pulumi.String(stringOr(args.Group, args.Account)), Mode: pulumi.String(stringOr(args.Mode, "0775")),
	}, opts...)
	if err != nil {
		return nil, err
	}

	childOpts, err := claudeChildOptions(opts, append([]pulumi.Resource{dir}, args.Shared.Resources...)...)
	if err != nil {
		return nil, err
	}
	links := make([]*Symlink, 0, len(args.Shared.Entries))
	for _, entry := range args.Shared.Entries {
		link, err := NewSymlink(ctx, name+"-"+entry.Name, &SymlinkArgs{
			Path:   pulumi.String(SharedTarget(args.Dir, entry.Name)),
			Target: pulumi.String(SharedTarget(args.Shared.Path, entry.Name)),
		}, childOpts...)
		if err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return &ClaudeProfile{Dir: args.Dir, ConfigDir: args.Dir, Links: links}, nil
}

func claudeChildOptions(opts []pulumi.ResourceOption, deps ...pulumi.Resource) ([]pulumi.ResourceOption, error) {
	// Node replaces dependsOn for each entry. Go appends dependencies when combining options.
	childOpts := make([]pulumi.ResourceOption, 0, len(opts)+1)
	for _, opt := range opts {
		snapshot, err := pulumi.NewResourceOptions(opt)
		if err != nil {
			return nil, err
		}
		if len(snapshot.DependsOn) == 0 && len(snapshot.DependsOnInputs) == 0 {
			childOpts = append(childOpts, opt)
		}
	}
	return append(childOpts, pulumi.DependsOn(deps)), nil
}

package homelab

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// AgentSpec describes a CLI installed from an archive or by its official installer.
type AgentSpec interface {
	install(agentInstallation) error
}

// ArchiveAgentSpec describes a checksummed archive and its executable.
type ArchiveAgentSpec struct {
	Key    string
	Bin    string
	URL    string
	SHA256 string
	Strip  int
}

// OfficialAgentSpec describes a vendor installer and where it leaves the executable.
type OfficialAgentSpec struct {
	Key       string
	Installer string
	Bin       string
	Link      bool
}

// T3Release describes the bootstrap build and its checksum.
type T3Release struct {
	Version string
	SHA256  string
	URL     func(version string) string
}

// T3Default is the bootstrap build used when T3CodeArgs.T3 is nil.
var T3Default = T3Release{
	Version: "0.1.13-nightly.20261125.5245",
	SHA256:  "de331775c0ef436edf446021c05b92d21787424efe5e8b33699c04710f175152",
	URL: func(version string) string {
		return "https://github.com/pingdotgg/t3code/releases/download/v" + version + "/t3-" + version + "-linux-x64.tar.gz"
	},
}

// AgentDefaults lists the CLIs installed when T3CodeArgs.Agents is nil.
var AgentDefaults = []AgentSpec{
	ArchiveAgentSpec{
		Key: "gh", Bin: "bin/gh",
		URL:    "https://github.com/cli/cli/releases/download/v2.106.0/gh_2.106.0_linux_amd64.tar.gz",
		SHA256: "a476449fa9625413c54a3734eded62e9e480e49f421ff08e7f3b3879c059a033", Strip: 1,
	},
	OfficialAgentSpec{Key: "claude", Installer: "curl -fsSL https://claude.ai/install.sh | bash", Bin: ".local/bin/claude"},
	OfficialAgentSpec{Key: "codex", Installer: "curl -fsSL https://chatgpt.com/codex/install.sh | CODEX_NON_INTERACTIVE=1 sh", Bin: ".local/bin/codex"},
	OfficialAgentSpec{Key: "opencode", Installer: "curl -fsSL https://opencode.ai/install | bash -s -- --no-modify-path", Bin: ".opencode/bin/opencode", Link: true},
}

// T3CodeArgs describes a T3 Code service account and its installation.
type T3CodeArgs struct {
	Account          *string
	Home             string
	Workspace        string
	Groups           []string
	UID              *int
	PrimaryGroup     *Group
	Git              T3GitArgs
	Agents           []AgentSpec
	T3               *T3Release
	Sudo             *T3SudoArgs
	DisableSudo      bool
	Unit             *UnitArgs
	AccountDependsOn []pulumi.Resource
}

// T3GitArgs supplies the entire git configuration and optional key comment.
type T3GitArgs struct {
	Config     string
	KeyComment *string
}

// T3SudoArgs selects the sudoers file. Set DisableSudo on T3CodeArgs to omit the rule.
type T3SudoArgs struct {
	File *string
}

// UnitArgs sets the service name, resource limits, and TMPDIR.
type UnitArgs struct {
	Name   *string
	Limits UnitLimits
	TmpDir string
}

// UnitLimits describes optional systemd service limits. Weights must be in [1, 10000].
type UnitLimits struct {
	MemoryHigh    string
	MemoryMax     string
	MemorySwapMax string
	OOMPolicy     string
	CPUWeight     *int
	IOWeight      *int
}

// UnitResourceLines renders the optional settings in systemd [Service] order.
func UnitResourceLines(unit *UnitArgs) (string, error) {
	if unit == nil {
		return "", nil
	}
	limits := unit.Limits
	lines := []string{}
	for _, setting := range []struct{ name, value string }{
		{"MemoryHigh", limits.MemoryHigh},
		{"MemoryMax", limits.MemoryMax},
		{"MemorySwapMax", limits.MemorySwapMax},
		{"OOMPolicy", limits.OOMPolicy},
	} {
		if setting.value != "" {
			lines = append(lines, setting.name+"="+setting.value)
		}
	}
	for _, weight := range []struct {
		name  string
		value *int
	}{
		{"CPUWeight", limits.CPUWeight},
		{"IOWeight", limits.IOWeight},
	} {
		if weight.value == nil {
			continue
		}
		if *weight.value < 1 || *weight.value > 10000 {
			return "", fmt.Errorf("%s must be an integer from 1 to 10000, not %d", weight.name, *weight.value)
		}
		lines = append(lines, weight.name+"="+strconv.Itoa(*weight.value))
	}
	if unit.TmpDir != "" {
		lines = append(lines, "Environment=TMPDIR="+unit.TmpDir)
	}
	if len(lines) == 0 {
		return "", nil
	}
	return "\n# What the agents may take of the machine, and where their scratch goes. Declared by the stack.\n" +
		strings.Join(lines, "\n") + "\n", nil
}

// T3Code holds paths, resource handles and the Git public key for an installation.
type T3Code struct {
	Account      string
	User         *User
	BinDirectory *Directory
	Home         string
	Bin          string
	Opt          string
	Workspace    string
	UnitName     string
	PublicKey    pulumi.StringOutput
}

// T3CodeResource declares the resources for a headless T3 Code system service.
func T3CodeResource(ctx *pulumi.Context, provider *Provider, args *T3CodeArgs) (*T3Code, error) {
	if args == nil || provider == nil {
		return nil, fmt.Errorf("T3CodeResource requires arguments and a provider")
	}
	unitLines, err := UnitResourceLines(args.Unit)
	if err != nil {
		return nil, err
	}
	const defaultAccount = "t3code"
	account := stringOr(args.Account, defaultAccount)
	named := func(base string) string {
		if account == defaultAccount {
			return base
		}
		return account + "-" + base
	}
	home, workspace := args.Home, args.Workspace
	bin, opt := home+"/.local/bin", home+"/.local/opt"
	unitName, keyHost := account, "homelab"
	if args.Unit != nil {
		unitName = stringOr(args.Unit.Name, account)
		keyHost = stringOr(args.Unit.Name, keyHost)
	}

	accountInput := pulumi.String(account)
	providerOpt := pulumi.Provider(provider)
	withDeps := func(deps ...pulumi.Resource) []pulumi.ResourceOption {
		return []pulumi.ResourceOption{providerOpt, pulumi.DependsOn(deps)}
	}
	userArgs := &UserArgs{
		Name: accountInput, Shell: pulumi.String("/bin/bash"), Home: pulumi.String(home),
		CreateHome: pulumi.Bool(true), Groups: pulumi.ToStringArray(args.Groups),
	}
	if args.UID != nil {
		userArgs.Uid = pulumi.Int(*args.UID)
	}
	userDeps := args.AccountDependsOn
	if args.PrimaryGroup != nil {
		userArgs.Group = accountInput
		userDeps = append([]pulumi.Resource{args.PrimaryGroup}, userDeps...)
	}
	user, err := NewUser(ctx, "user-"+account, userArgs, withDeps(userDeps...)...)
	if err != nil {
		return nil, err
	}

	directory := func(name, path, mode string, dep pulumi.Resource) (*Directory, error) {
		return NewDirectory(ctx, "dir-"+account+"-"+name, &DirectoryArgs{
			Path: pulumi.String(path), Owner: accountInput, Group: accountInput, Mode: pulumi.String(mode),
		}, withDeps(dep)...)
	}
	if _, err := directory("home", home, "0700", user); err != nil {
		return nil, err
	}
	if _, err := NewAptPackages(ctx, "pkg-"+account+"-tools", &AptPackagesArgs{
		Names: pulumi.ToStringArray([]string{"git", "curl", "ca-certificates", "unzip"}),
	}, providerOpt); err != nil {
		return nil, err
	}
	dotlocal, err := directory("dotlocal", home+"/.local", "0755", user)
	if err != nil {
		return nil, err
	}
	dott3, err := directory("dott3", home+"/.t3", "0700", user)
	if err != nil {
		return nil, err
	}
	runtime, err := directory("runtime", home+"/.t3/runtime", "0755", dott3)
	if err != nil {
		return nil, err
	}
	versions, err := directory("versions", home+"/.t3/runtime/versions", "0755", runtime)
	if err != nil {
		return nil, err
	}
	binDir, err := directory("bin", bin, "0755", dotlocal)
	if err != nil {
		return nil, err
	}
	optDir, err := directory("opt", opt, "0755", dotlocal)
	if err != nil {
		return nil, err
	}

	release := T3Default
	if args.T3 != nil {
		release = *args.T3
		if release.URL == nil {
			release.URL = T3Default.URL
		}
	}
	t3Prefix := home + "/.t3/runtime/versions/" + release.Version
	t3, err := registerAgentArchive(ctx, named("archive-t3"), t3Prefix, accountInput, ArchiveAgentSpec{
		Key: "t3", Bin: "t3", URL: release.URL(release.Version), SHA256: release.SHA256, Strip: 1,
	}, withDeps(user, versions)...)
	if err != nil {
		return nil, err
	}
	managedFile := func(name, path, content string, dep pulumi.Resource) error {
		_, err := NewManagedFile(ctx, name, &ManagedFileArgs{
			Path: pulumi.String(path), Content: pulumi.String(content), Mode: pulumi.String("0644"),
			Owner: accountInput, Group: accountInput,
		}, withDeps(dep)...)
		return err
	}
	if err := managedFile(named("file-t3-install-complete"), t3Prefix+"/.install-complete", release.Version+"\n", t3); err != nil {
		return nil, err
	}
	if _, err := NewSymlink(ctx, named("link-t3"), &SymlinkArgs{
		Path: pulumi.String(bin + "/t3"), Target: pulumi.String(t3Prefix + "/t3"), AcceptLauncherScript: pulumi.Bool(true),
	}, providerOpt, pulumi.DependsOn([]pulumi.Resource{t3, binDir}), pulumi.IgnoreChanges([]string{"target"})); err != nil {
		return nil, err
	}

	agents := args.Agents
	if agents == nil {
		agents = AgentDefaults
	}
	installation := agentInstallation{
		ctx: ctx, named: named, options: withDeps, account: accountInput,
		home: home, bin: bin, opt: opt, user: user, binDir: binDir, optDir: optDir,
	}
	for _, agent := range agents {
		if err := agent.install(installation); err != nil {
			return nil, err
		}
	}

	dotssh, err := directory("dotssh", home+"/.ssh", "0700", user)
	if err != nil {
		return nil, err
	}
	key, err := NewSshKey(ctx, "sshkey-"+account+"-git", &SshKeyArgs{
		Path: pulumi.String(home + "/.ssh/id_ed25519"), Type: pulumi.String("ed25519"),
		Comment: pulumi.String(stringOr(args.Git.KeyComment, account+"@"+keyHost)), Owner: accountInput, Group: accountInput,
	}, withDeps(dotssh)...)
	if err != nil {
		return nil, err
	}
	if err := managedFile("file-"+account+"-gitconfig", home+"/.gitconfig", args.Git.Config, user); err != nil {
		return nil, err
	}
	if !args.DisableSudo {
		file := "030_" + account
		if args.Sudo != nil {
			file = stringOr(args.Sudo.File, file)
		}
		if _, err := NewSudoRule(ctx, "sudo-"+account+"-apt", &SudoRuleArgs{
			File: pulumi.String(file), User: accountInput, RunAs: pulumi.String("root"), Passwordless: pulumi.Bool(true),
		}, withDeps(user)...); err != nil {
			return nil, err
		}
	}
	if _, err := NewSystemdUnit(ctx, "unit-"+account, &SystemdUnitArgs{
		Name: pulumi.String(unitName), Enabled: pulumi.Bool(true), Started: pulumi.Bool(true),
		Unit: pulumi.String(t3SystemdUnit(unitName, account, workspace, bin, home, unitLines)),
	}, withDeps(t3, versions, binDir)...); err != nil {
		return nil, err
	}
	return &T3Code{
		Account: account, User: user, BinDirectory: binDir, Home: home, Bin: bin, Opt: opt,
		Workspace: workspace, UnitName: unitName, PublicKey: key.PublicKey,
	}, nil
}

type agentInstallation struct {
	ctx            *pulumi.Context
	named          func(string) string
	options        func(...pulumi.Resource) []pulumi.ResourceOption
	account        pulumi.String
	home, bin, opt string
	user           *User
	binDir, optDir *Directory
}

func (spec ArchiveAgentSpec) install(a agentInstallation) error {
	prefix := a.opt + "/" + spec.Key
	archive, err := registerAgentArchive(a.ctx, a.named("archive-"+spec.Key), prefix, a.account, spec, a.options(a.user, a.optDir)...)
	if err != nil {
		return err
	}
	_, err = NewSymlink(a.ctx, a.named("link-"+spec.Key), &SymlinkArgs{
		Path: pulumi.String(a.bin + "/" + spec.Key), Target: pulumi.String(prefix + "/" + spec.Bin),
	}, a.options(archive, a.binDir)...)
	return err
}

func (spec OfficialAgentSpec) install(a agentInstallation) error {
	installed, err := NewOfficialInstall(a.ctx, a.named("install-"+spec.Key), &OfficialInstallArgs{
		Account: a.account, Home: pulumi.String(a.home), Installer: pulumi.String(spec.Installer),
		Bin: pulumi.String(a.home + "/" + spec.Bin),
	}, a.options(a.user, a.binDir)...)
	if err != nil || !spec.Link {
		return err
	}
	_, err = NewSymlink(a.ctx, a.named("link-"+spec.Key), &SymlinkArgs{
		Path: pulumi.String(a.bin + "/" + spec.Key), Target: pulumi.String(a.home + "/" + spec.Bin),
	}, a.options(installed)...)
	return err
}

func registerAgentArchive(ctx *pulumi.Context, name, prefix string, account pulumi.String, spec ArchiveAgentSpec, opts ...pulumi.ResourceOption) (*Archive, error) {
	command := pulumi.String("{}/" + spec.Bin + " --version")
	return NewArchive(ctx, name, &ArchiveArgs{
		Name: pulumi.String(spec.Key), Url: pulumi.String(spec.URL), Sha256: pulumi.String(spec.SHA256),
		Prefix: pulumi.String(prefix), Strip: pulumi.Int(spec.Strip), Binary: pulumi.String(spec.Bin),
		HealthCommand: command, VersionCommand: command, Owner: account, Group: account,
	}, opts...)
}

// Keep the unit text byte-for-byte compatible with the Node SDK.
func t3SystemdUnit(unitName, account, workspace, bin, home, unitLines string) string {
	return fmt.Sprintf(`# /etc/systemd/system/%[1]s.service
# Managed by Pulumi, from the homelab-server stack. Do not run `+"`"+`t3 service install`+"`"+`: it writes a
# competing user unit and this file would then be one of two owners of the same service.
[Unit]
Description=T3 Code, headless agent harness
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=%[2]s
Group=%[2]s
# Where the add-project dialog proposes to clone is the server setting `+"`"+`addProjectBaseDirectory`+"`"+`,
# surfaced in the UI as "Add project base directory". `+"`"+`~/`+"`"+` is its EMPTY fallback, not a derivation
# from HOME -- so it is unconfigured rather than unconfigurable. It is deliberately not declared
# here: it persists in ~/.t3/userdata/settings.json alongside every other setting the app writes,
# so a ManagedFile owning that path would revert Chris's UI changes on each deployment with
# nothing to connect the symptom to the cause. Set once through the app instead.
#
# Three things that look like the answer and are not, all tested against the binary: T3CODE_BASE_DIR
# does not occur in it at all; --base-dir and T3CODE_HOME are T3's own DATA directory, so pointing
# them here would move the SQLite database and four services' OAuth credentials into a directory
# full of checkouts; and T3CODE_PROJECT_ROOT is something T3 SETS for child processes from the
# project's cwd -- declaring it changed nothing, which was confirmed by trying it. Three
# candidates were tested against the binary and all three are wrong: T3CODE_BASE_DIR does not
# appear in it at all; --base-dir and T3CODE_HOME are T3's own DATA directory, so pointing them
# here would move the SQLite database and four services' OAuth credentials into a directory full
# of checkouts; and T3CODE_PROJECT_ROOT is something T3 SETS for child processes from the
# project's cwd, not something it reads. The destination is chosen in the dialog.
#
# The workspace, not the home. `+"`"+`t3 serve`+"`"+` takes a working directory as a positional argument and
# otherwise inherits this one, and the add-project flow offers a destination derived from it -- so
# with the home here it proposes cloning into ~/<repo>, which is the directory holding this
# account's OAuth credentials and its SQLite database, at 0700. Checkouts belong in the one place
# the account has a deliberate write grant.
WorkingDirectory=%[3]s

# The launcher. NOT a path under .t3/runtime/versions -- see the comment above this resource.
# The trailing path is `+"`"+`cwd`+"`"+`: the working directory for provider sessions.
ExecStart=%[4]s/t3 serve %[3]s

# A system unit inherits neither of these. HOME is set explicitly rather than left for systemd to
# derive from User=, and PATH must contain the provider CLIs because t3 discovers claude, codex and
# opencode by looking for them there -- the default unit PATH does not include this directory, so
# without this the harness starts cleanly and then reports that no providers are installed.
Environment=HOME=%[5]s
# %[5]s/.local/share/mise/shims comes first because that is how mise exposes what an agent
# installs: a `+"`"+`go`+"`"+` or `+"`"+`node`+"`"+` it fetched is a shim there, not a binary in ~/.local/bin. Without it
# an agent installs a toolchain successfully and the service still cannot find it, which reads as
# the install having failed.
Environment=PATH=%[5]s/.local/share/mise/shims:%[4]s:/usr/local/bin:/usr/bin:/bin

# Set explicitly rather than left to the default, because it is the thing that silently broke
# the sudo grant once and an absent line does not tell a reader a decision was made.
#
# The name is a double negative: `+"`"+`no`+"`"+` means "do NOT forbid new privileges", i.e. sudo works. It
# reads back in /proc/<pid>/status as `+"`"+`NoNewPrivs: 0`+"`"+` -- systemd's boolean and the kernel's
# PR_SET_NO_NEW_PRIVS flag are different things describing the same state, which is why the check
# below is on the kernel's value and not on this line.
NoNewPrivileges=no
#
# `+"`"+`yes`+"`"+` was right while the account had no sudo, to close the last route to privilege. It is
# incompatible with the sudo grant above: it blocks every setuid binary, and setuid is how sudo
# escalates -- so with it set, sudo refuses before it ever reads /etc/sudoers.d, and an agent sees
# a permission failure that no sudoers rule can fix.
#
# Worth knowing how this hides: `+"`"+`sudo -u t3code ... sudo apt-get`+"`"+` over ssh SUCCEEDS, because a
# login shell is not a child of this unit and does not inherit the flag. Only processes the
# service spawns are affected. So the account tests fine and the agents still cannot install
# anything. Verify with NoNewPrivs in /proc/<pid>/status, not with a shell.

Restart=always
RestartSec=5
%[6]s
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
`, unitName, account, workspace, bin, home, unitLines)
}

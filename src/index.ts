/**
 * homelab — Pulumi resources for a Linux machine you own.
 *
 * There is a gap between the tools that provision infrastructure and the tools that configure it.
 * Pulumi is excellent at the first and has nothing of its own for the second: `remote.Command` runs
 * shell over SSH and records that it ran, which is not the same as knowing what the machine is.
 * Ansible fills the gap and is a YAML dialect; NixOS closes it entirely and is a project in itself.
 *
 * This is the small answer. Desired state on a machine you own, over SSH, in TypeScript, with one
 * rule that decides everything else:
 *
 *   **Every resource implements a real `read`.**
 *
 * Not "it ran once", but "here is what the machine says right now". That is what makes
 * `pulumi up --refresh` mean something: a file somebody edited by hand, a service somebody stopped
 * last Tuesday, a package somebody removed — all of it comes back as drift you can see, instead of
 * the code and the machine parting company in silence.
 *
 * What it deliberately does not do is claim the whole machine. Anything not declared is left
 * exactly as it is, which is what makes it safe to point at a server that already works and has
 * been hand-tended for years. The cost of that is honest and worth stating: a machine managed this
 * way is reproducible in the parts you modelled and no further.
 */

export { ask, must, asRoot, escalate, shellQuote, heredoc, heredocInto, sshArgs, type Host, type Ran } from './ssh.ts';
export { sshTransport, describe, type Transport, type Target } from './ssh.ts';
export { localTransport, type LocalOptions } from './local.ts';
export { disagreeing, type Resolved, type FileOnHost } from './resolved.ts';
export { normaliseMode } from './mode.ts';
export { FluxApp, ignoreRules, type FluxAppArgs } from './flux/index.ts';
export { fluxReady, fluxReason, type FluxCheckArgs } from './flux/ready.ts';
export { providerChanged, withLegacyAlias } from './upgrade.ts';
export { mountedAt } from './checks.ts';
export { ManagedFile, readFile, writeFile, parseFileStat, type FileArgs } from './resources/file.ts';
export { Directory, readDirectory, parseStat, type DirectoryArgs } from './resources/directory.ts';
export { Symlink, readSymlink, interpretSymlink, type SymlinkArgs } from './resources/symlink.ts';
export {
  FstabEntry,
  readFstabEntry,
  fstabLine,
  findEntry,
  targetOf,
  upsertFstab,
  removeFromFstab,
  type FstabEntryArgs,
} from './resources/fstab.ts';
export { AptPackage, readPackage, parseDpkgStatus, type AptPackageArgs } from './resources/apt.ts';
export { DebPackage, installScript, type DebPackageArgs } from './resources/deb.ts';
export {
  RcloneRemote,
  readRemote,
  parseDump,
  configPairs,
  type RcloneRemoteArgs,
} from './resources/rclone.ts';
export {
  AptPackages,
  readPackages,
  parseInstalled,
  parseSimulatedRemovals,
  missingFrom,
  type AptPackagesArgs,
} from './resources/packages.ts';
export { SystemdUnit, readUnit, parseShow, type SystemdUnitArgs } from './resources/systemd.ts';
export {
  SystemdInstance,
  readInstance,
  escapeInstance,
  instanceUnit,
  type SystemdInstanceArgs,
} from './resources/instance.ts';
export { User, readUser, groupsToLose, type UserArgs } from './resources/user.ts';
export {
  AuthorizedKey,
  readAuthorizedKey,
  keyBody,
  keyComment,
  authorizedLine,
  upsertAuthorized,
  removeAuthorized,
  type AuthorizedKeyArgs,
} from './resources/authorizedkey.ts';
export {
  SshdConfig,
  readSshdConfig,
  parseSshdT,
  normaliseSshdValue,
  renderSshd,
  keywordFor,
  hasInclude,
  sshdDropIn,
  type SshdConfigArgs,
  type SshdSettings,
  type SshdMatch,
} from './resources/sshd.ts';
export {
  SshKey,
  readSshKey,
  parseFingerprint,
  parseHostKeys,
  hostKeys,
  type SshKeyArgs,
} from './resources/sshkey.ts';
export { Group, readGroup, parseGroupEntry, type GroupArgs } from './resources/group.ts';
export {
  Hostname,
  readHostname,
  hostsName,
  hostsNames,
  hostsNamesHost,
  parseHostnameOutput,
  setHostsName,
  type HostnameArgs,
} from './resources/hostname.ts';
export {
  SambaShare,
  SambaSetting,
  SambaUser,
  readSetting,
  sambaSameValue,
  upsertSetting,
  removeSetting,
  readShare,
  readSambaUser,
  effectiveShare,
  shareSection,
  upsertSection,
  removeSection,
  parseSections,
  parseShareSettings,
  parseSambaUsers,
  type SambaShareArgs,
  type SambaSettingArgs,
  type SambaUserArgs,
} from './resources/samba.ts';
export {
  SudoRule,
  readSudoRule,
  sudoersFile,
  sudoersLine,
  sudoersFileName,
  type SudoRuleArgs,
} from './resources/sudo.ts';
export { Precondition, readPrecondition, checkCommand, type PreconditionArgs } from './resources/precondition.ts';
export { Swap, readSwap, parseProcSwaps, parseFstabSwap, type SwapArgs } from './resources/swap.ts';
export {
  Journald,
  readJournald,
  journaldFile,
  parseEffective,
  overriddenBy,
  type JournaldArgs,
} from './resources/journald.ts';
export {
  BootConfig,
  readBootConfig,
  parseBootSections,
  applyToSection,
  removeFromSection as removeFromBootSection,
  parseVcgencmd,
  renderSettings,
  readOverlays,
  overlayNameOf,
  unknownOverlays,
  BOOT_CANDIDATES,
  type BootConfigArgs,
  type BootSettings,
} from './resources/bootconfig.ts';
export {
  KernelCmdline,
  readCmdline,
  merge as mergeCmdline,
  bootedWith,
  CANDIDATES,
  type KernelCmdlineArgs,
} from './resources/cmdline.ts';
export {
  audit,
  type Declared,
  type AuditPaths,
  type AuditFindings,
  type PackageFinding,
  type UserFinding,
} from './audit.ts';

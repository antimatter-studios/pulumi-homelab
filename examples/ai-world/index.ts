import * as pulumi from '@pulumi/pulumi';
import { AptPackage } from './resources/apt';
import { ManagedFile } from './resources/file';
import { SystemdUnit } from './resources/systemd';
import { User } from './resources/user';
import type { Host } from './ssh';

/**
 * The home server, described.
 *
 * One stack per machine — `pi`, `nas`, and so on — because a stack's state remembers what it built
 * on the machine it built it on. Point an existing stack at a different host and it reconciles the
 * new machine against the old one's memory: it believes the file is already there because it wrote
 * it, on a box across the room. New machine, new stack, without exception.
 *
 * Nothing here describes the whole machine, and it is not meant to. Anything not modelled is left
 * exactly as it is, which is what makes this safe to point at a server that already works and has
 * been hand-tended for years.
 */

const settings = new pulumi.Config();

const host: Host = {
  address: settings.require('host'),
  user: settings.get('user') ?? 'chris',
};

/** Where the game lives on the server, and who runs it. */
const HOME = '/opt/ai.world';
const SERVICE_USER = 'aiworld';
const PORT = settings.getNumber('port') ?? 8787;

/**
 * Node, from the distribution's own packages.
 *
 * Debian's node lags, and the tempting answer is a NodeSource script piped into a shell. That is a
 * command with no readable state — nothing could tell you afterwards which version is installed or
 * whether the repository is still configured — so it is exactly the thing this provider exists to
 * avoid. If the packaged version is ever too old, the honest fix is a resource that models an apt
 * repository, not a curl into bash.
 */
const node = new AptPackage('nodejs', host, { name: 'nodejs', update: true });

/**
 * The unit.
 *
 * `DynamicUser` is deliberately not used: the game writes worlds into its data directory and those
 * have to survive a restart, which a per-boot user id would quietly break. The hardening below is
 * the ordinary set — it can read the system, write nothing but its own data, and gains nothing by
 * being root, which it never is.
 *
 * It runs one file. `server/build.config.ts` already rolls the whole server into a single
 * `server.mjs` with only `ws` left outside, so the machine needs node and two things copied to it
 * rather than a checkout, a package manager and a compiler.
 */
const unit = `[Unit]
Description=ai.world — the world, and the page that draws it
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${SERVICE_USER}
WorkingDirectory=${HOME}
Environment=PORT=${PORT}
Environment=DATA_DIR=${HOME}/data
Environment=STATIC_DIR=${HOME}/dist
ExecStart=/usr/bin/node ${HOME}/server.mjs
Restart=on-failure
RestartSec=3
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=${HOME}/data

[Install]
WantedBy=multi-user.target
`;

/**
 * Who the game runs as.
 *
 * Its own account rather than the login user, so a compromise of the thing facing the internet is a
 * compromise of a shell-less account that owns one directory. `nologin` because nothing should ever
 * be logging in as it, and a service account with a real shell is an invitation nobody sent.
 */
const owner = new User('aiworld', host, {
  name: SERVICE_USER,
  home: HOME,
  shell: '/usr/sbin/nologin',
  createHome: true,
});

const service = new SystemdUnit('aiworld', host, {
  name: 'aiworld',
  unit,
  enabled: true,
  started: true,
}, { dependsOn: [node, owner] });

/**
 * A note on the machine saying what put it there.
 *
 * Small, and worth its place: the next person to log in — including you, in a year — finds out that
 * this box is described somewhere rather than hand-built, before they start editing files that will
 * be quietly reverted on the next deployment.
 */
new ManagedFile('provenance', host, {
  path: '/etc/ai.world.provenance',
  content: `This machine's ai.world service is managed by Pulumi.
Editing it by hand will show up as drift on the next \`pulumi up --refresh\`,
and will be overwritten. The description lives in infra/ of the ai.world repo.
`,
  mode: '0644',
});

export const serving = pulumi.interpolate`http://${host.address}:${PORT}/`;
export const runs = service.started;
export const nodeVersion = node.version;

/**
 * `apt-get`, made safe to run unattended.
 *
 * **It waits for dpkg's lock instead of failing on it.** Pulumi creates independent resources in
 * parallel, so on a fresh machine several package resources ask for the lock in the same second.
 * Without a timeout every one but the first fails at once with "Could not get lock
 * /var/lib/dpkg/lock-frontend", and so does anything that meets unattended-upgrades halfway through
 * a run. Ten minutes is longer than any install here takes.
 *
 * **It keeps a configuration file that is already there.** Pulumi often writes a package's
 * configuration before the package arrives (containerd's config.toml, for one), and dpkg then asks
 * whether to keep it or take the maintainer's version. `DEBIAN_FRONTEND=noninteractive` does not
 * cover that question: dpkg waits on it for ever, holding the lock, and the deployment hangs with
 * nothing in its output. `confdef` takes the default where there is one and `confold` keeps the
 * file otherwise, which is the file Pulumi declared.
 */
export const APT_GET = 'apt-get -o DPkg::Lock::Timeout=600 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold';

import * as pulumi from "@pulumi/pulumi";
import * as inputs from "./types/input";
import * as outputs from "./types/output";
/**
 * A reverse ssh tunnel kept up by autossh under systemd, checked against the running process.
 */
export declare class SshTunnel extends pulumi.CustomResource {
    /**
     * Get an existing SshTunnel resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): SshTunnel;
    /**
     * Returns true if the given object is an instance of SshTunnel.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is SshTunnel;
    /**
     * Whether systemd says the unit is active.
     */
    readonly active: pulumi.Output<boolean>;
    readonly directory: pulumi.Output<string>;
    /**
     * The unit file as the machine has it.
     */
    readonly file: pulumi.Output<string>;
    readonly forwards: pulumi.Output<outputs.provider.TunnelForward[]>;
    readonly identity: pulumi.Output<string>;
    readonly port: pulumi.Output<number>;
    readonly restartSec: pulumi.Output<number>;
    /**
     * How many times systemd has restarted the unit.
     */
    readonly restarts: pulumi.Output<number>;
    readonly runAs: pulumi.Output<string>;
    /**
     * The forwards the running process carries, read from /proc.
     */
    readonly running: pulumi.Output<outputs.provider.TunnelForward[]>;
    readonly to: pulumi.Output<string>;
    readonly unit: pulumi.Output<string>;
    /**
     * Create a SshTunnel resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: SshTunnelArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a SshTunnel resource.
 */
export interface SshTunnelArgs {
    /**
     * Where the unit file goes.
     */
    directory?: pulumi.Input<string>;
    /**
     * The reverse forwards, all carried on one connection.
     */
    forwards: pulumi.Input<pulumi.Input<inputs.provider.TunnelForwardArgs>[]>;
    /**
     * The private key the tunnel offers, and the only one.
     */
    identity: pulumi.Input<string>;
    /**
     * The far end's ssh port.
     */
    port?: pulumi.Input<number>;
    /**
     * Seconds between restarts. At least 5: a tight loop of failing logins gets the address banned.
     */
    restartSec?: pulumi.Input<number>;
    /**
     * The account the tunnel runs as, whose known_hosts must trust the far end.
     */
    runAs: pulumi.Input<string>;
    /**
     * Where the tunnel goes, as user@host.
     */
    to: pulumi.Input<string>;
    /**
     * The unit name, without .service. Derived from the destination and port when not given.
     */
    unit?: pulumi.Input<string>;
}

import * as pulumi from "@pulumi/pulumi";
import * as outputs from "./types/output";
/**
 * The machine's name, with /etc/hosts agreeing with it.
 */
export declare class Hostname extends pulumi.CustomResource {
    /**
     * Get an existing Hostname resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): Hostname;
    /**
     * Returns true if the given object is an instance of Hostname.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is Hostname;
    readonly domain: pulumi.Output<string>;
    readonly effective: pulumi.Output<outputs.provider.HostnameEffective>;
    readonly hosts: pulumi.Output<string>;
    readonly name: pulumi.Output<string>;
    readonly restartAvahi: pulumi.Output<boolean>;
    /**
     * Create a Hostname resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: HostnameArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a Hostname resource.
 */
export interface HostnameArgs {
    /**
     * A domain, written as <name>.<domain> before the short name on the 127.0.1.1 line.
     */
    domain?: pulumi.Input<string>;
    /**
     * The hosts file, /etc/hosts by default.
     */
    hosts?: pulumi.Input<string>;
    /**
     * The short name.
     */
    name: pulumi.Input<string>;
    /**
     * Whether avahi-daemon is restarted after a rename, so mDNS announces the new name. On by default.
     */
    restartAvahi?: pulumi.Input<boolean>;
}

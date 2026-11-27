import * as pulumi from "@pulumi/pulumi";
import * as inputs from "./types/input";
import * as outputs from "./types/output";
/**
 * Named entries on a POSIX ACL, set and removed without touching anybody else's.
 */
export declare class PosixAcl extends pulumi.CustomResource {
    /**
     * Get an existing PosixAcl resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): PosixAcl;
    /**
     * Returns true if the given object is an instance of PosixAcl.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is PosixAcl;
    /**
     * What getfacl grants for each declared access entry, effective permissions included.
     */
    readonly actual: pulumi.Output<outputs.provider.AclEntryActual[]>;
    /**
     * What getfacl grants for each declared default entry.
     */
    readonly defaultActual: pulumi.Output<outputs.provider.AclEntryActual[]>;
    readonly defaultEntries: pulumi.Output<outputs.provider.AclEntrySettled[]>;
    readonly entries: pulumi.Output<outputs.provider.AclEntrySettled[]>;
    readonly normalise: pulumi.Output<outputs.provider.AclNormaliseArgs | undefined>;
    /**
     * How many directories the last normalise changed.
     */
    readonly normalised: pulumi.Output<number | undefined>;
    readonly path: pulumi.Output<string>;
    /**
     * Create a PosixAcl resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: PosixAclArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a PosixAcl resource.
 */
export interface PosixAclArgs {
    /**
     * Default entries, which new entries under a directory inherit.
     */
    defaultEntries?: pulumi.Input<pulumi.Input<inputs.provider.AclEntryArgsArgs>[]>;
    /**
     * Access entries: named users and groups.
     */
    entries?: pulumi.Input<pulumi.Input<inputs.provider.AclEntryArgsArgs>[]>;
    /**
     * Correct directories under the path that have one exact mode and group to another mode.
     */
    normalise?: pulumi.Input<inputs.provider.AclNormaliseArgsArgs>;
    /**
     * The path whose ACL this sets.
     */
    path: pulumi.Input<string>;
}

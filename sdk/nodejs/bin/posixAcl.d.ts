import * as pulumi from "@pulumi/pulumi";
import * as inputs from "./types/input";
import * as outputs from "./types/output";
/**
 * Entries on a POSIX ACL, and optionally the owner and group, set and read back without touching the content.
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
    /**
     * Named default entries on the path that are not declared, for an exact declaration.
     */
    readonly defaultExtra: pulumi.Output<outputs.provider.AclEntryActual[] | undefined>;
    readonly entries: pulumi.Output<outputs.provider.AclEntrySettled[]>;
    readonly exact: pulumi.Output<boolean | undefined>;
    /**
     * Named access entries on the path that are not declared, for an exact declaration.
     */
    readonly extra: pulumi.Output<outputs.provider.AclEntryActual[] | undefined>;
    /**
     * The owning group getfacl reports, while one is declared.
     */
    readonly group: pulumi.Output<string | undefined>;
    readonly normalise: pulumi.Output<outputs.provider.AclNormaliseArgs | undefined>;
    /**
     * How many directories the last normalise changed.
     */
    readonly normalised: pulumi.Output<number | undefined>;
    /**
     * The owning user getfacl reports, while one is declared.
     */
    readonly owner: pulumi.Output<string | undefined>;
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
     * The path's named entries, access and default, are exactly the declared ones: any other is removed, as setfacl --set would, and one that appears later is drift. Without it, entries this resource did not declare are left alone.
     */
    exact?: pulumi.Input<boolean>;
    /**
     * The owning group, by name, chowned and read back from getfacl. Left alone when absent.
     */
    group?: pulumi.Input<string>;
    /**
     * Correct directories under the path that have one exact mode and group to another mode.
     */
    normalise?: pulumi.Input<inputs.provider.AclNormaliseArgsArgs>;
    /**
     * The owning user, by name, chowned and read back from getfacl. Left alone when absent.
     */
    owner?: pulumi.Input<string>;
    /**
     * The path whose ACL this sets.
     */
    path: pulumi.Input<string>;
}

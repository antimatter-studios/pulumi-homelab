import * as pulumi from "@pulumi/pulumi";
import * as inputs from "./types/input";
import * as outputs from "./types/output";
/**
 * A secret file on the machine, described by where it comes from rather than by what it says.
 */
export declare class ProtectedFile extends pulumi.CustomResource {
    /**
     * Get an existing ProtectedFile resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): ProtectedFile;
    /**
     * Returns true if the given object is an instance of ProtectedFile.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is ProtectedFile;
    /**
     * SHA-256 of the content under always, and the word "seeded" under once. The only thing here derived from the content.
     */
    readonly digest: pulumi.Output<string>;
    readonly enforce: pulumi.Output<string>;
    readonly group: pulumi.Output<string>;
    readonly mode: pulumi.Output<string>;
    readonly owner: pulumi.Output<string>;
    readonly path: pulumi.Output<string>;
    readonly source: pulumi.Output<outputs.provider.Source>;
    /**
     * Create a ProtectedFile resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: ProtectedFileArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a ProtectedFile resource.
 */
export interface ProtectedFileArgs {
    /**
     * always: keep the file as the source says, and report a hand edit or a rotated source as drift. once: write it when absent and never rewrite it, for material the application itself rotates.
     */
    enforce?: pulumi.Input<string>;
    /**
     * Owning group.
     */
    group?: pulumi.Input<string>;
    /**
     * Octal: '0600'.
     */
    mode?: pulumi.Input<string>;
    /**
     * Owning user.
     */
    owner?: pulumi.Input<string>;
    /**
     * Absolute path of the file on the machine. A new path is a new secret.
     */
    path: pulumi.Input<string>;
    /**
     * Where the content comes from. Not the content.
     */
    source: pulumi.Input<inputs.provider.SourceArgs>;
}

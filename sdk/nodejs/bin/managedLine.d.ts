import * as pulumi from "@pulumi/pulumi";
/**
 * One line in a file somebody else owns, found again by its marker.
 */
export declare class ManagedLine extends pulumi.CustomResource {
    /**
     * Get an existing ManagedLine resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): ManagedLine;
    /**
     * Returns true if the given object is an instance of ManagedLine.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is ManagedLine;
    /**
     * The line as the file has it, marker and all.
     */
    readonly actual: pulumi.Output<string>;
    readonly anchor: pulumi.Output<string>;
    readonly comment: pulumi.Output<string>;
    readonly line: pulumi.Output<string>;
    readonly marker: pulumi.Output<string>;
    readonly path: pulumi.Output<string>;
    /**
     * Whether it is on the right side of its anchor.
     */
    readonly placed: pulumi.Output<boolean>;
    readonly position: pulumi.Output<string>;
    /**
     * Create a ManagedLine resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: ManagedLineArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a ManagedLine resource.
 */
export interface ManagedLineArgs {
    /**
     * The text the line is placed relative to, for before and after.
     */
    anchor?: pulumi.Input<string>;
    /**
     * The file's comment syntax, # by default.
     */
    comment?: pulumi.Input<string>;
    /**
     * The line, without its marker.
     */
    line: pulumi.Input<string>;
    /**
     * What finds the line again, written into it as a trailing comment.
     */
    marker: pulumi.Input<string>;
    /**
     * The file the line goes in. It must exist; this never creates it.
     */
    path: pulumi.Input<string>;
    /**
     * prepend, append (the default), before or after the anchor.
     */
    position?: pulumi.Input<string>;
}

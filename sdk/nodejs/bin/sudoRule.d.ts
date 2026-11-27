import * as pulumi from "@pulumi/pulumi";
/**
 * A sudo privilege the machine should grant, checked against the file rather than remembered.
 */
export declare class SudoRule extends pulumi.CustomResource {
    /**
     * Get an existing SudoRule resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): SudoRule;
    /**
     * Returns true if the given object is an instance of SudoRule.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is SudoRule;
    readonly commands: pulumi.Output<string[] | undefined>;
    readonly content: pulumi.Output<string>;
    readonly directory: pulumi.Output<string>;
    readonly file: pulumi.Output<string>;
    readonly mode: pulumi.Output<string>;
    readonly owner: pulumi.Output<string>;
    /**
     * Create a SudoRule resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: SudoRuleArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a SudoRule resource.
 */
export interface SudoRuleArgs {
    /**
     * What may be run, as sudoers spells it. Full paths, or ['ALL'] (the default).
     */
    commands?: pulumi.Input<pulumi.Input<string>[]>;
    /**
     * Which drop-in directory, for a machine that does not keep it at /etc/sudoers.d.
     */
    directory?: pulumi.Input<string>;
    /**
     * The file's name in the drop-in directory. Defaults to the resource's own name.
     */
    file?: pulumi.Input<string>;
    /**
     * A group instead, written %wheel in sudoers and given here without the %.
     */
    group?: pulumi.Input<string>;
    /**
     * Whether the commands run without a password. Required rather than defaulted.
     */
    passwordless: pulumi.Input<boolean>;
    /**
     * Who the commands may be run as. ALL by default.
     */
    runAs?: pulumi.Input<string>;
    /**
     * The account this is about. Exactly one of user or group.
     */
    user?: pulumi.Input<string>;
}

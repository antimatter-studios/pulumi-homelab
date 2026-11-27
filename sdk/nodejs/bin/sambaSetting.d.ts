import * as pulumi from "@pulumi/pulumi";
/**
 * One samba parameter, written into its section of smb.conf.
 */
export declare class SambaSetting extends pulumi.CustomResource {
    /**
     * Get an existing SambaSetting resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): SambaSetting;
    /**
     * Returns true if the given object is an instance of SambaSetting.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is SambaSetting;
    /**
     * The value as the file says it.
     */
    readonly actual: pulumi.Output<string>;
    readonly apply: pulumi.Output<string>;
    readonly config: pulumi.Output<string>;
    /**
     * The value as testparm -sv reports it, defaults included.
     */
    readonly effective: pulumi.Output<string>;
    readonly key: pulumi.Output<string>;
    readonly share: pulumi.Output<string>;
    readonly value: pulumi.Output<string>;
    /**
     * Create a SambaSetting resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: SambaSettingArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a SambaSetting resource.
 */
export interface SambaSettingArgs {
    /**
     * How a change takes effect: reload (smbcontrol reload-config), nmbd (also restart nmbd) or all (restart nmbd and smbd).
     */
    apply?: pulumi.Input<string>;
    /**
     * The smb.conf to edit.
     */
    config?: pulumi.Input<string>;
    /**
     * The parameter name.
     */
    key: pulumi.Input<string>;
    /**
     * The section, as in global.
     */
    share: pulumi.Input<string>;
    /**
     * Its value.
     */
    value: pulumi.Input<string>;
}

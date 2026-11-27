import * as pulumi from "@pulumi/pulumi";
/**
 * Parameters on the kernel command line. Takes effect at the next boot.
 */
export declare class KernelCmdline extends pulumi.CustomResource {
    /**
     * Get an existing KernelCmdline resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): KernelCmdline;
    /**
     * Returns true if the given object is an instance of KernelCmdline.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is KernelCmdline;
    /**
     * The whole line as the file has it.
     */
    readonly cmdline: pulumi.Output<string>;
    readonly flags: pulumi.Output<string[]>;
    readonly path: pulumi.Output<string>;
    /**
     * Create a KernelCmdline resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: KernelCmdlineArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a KernelCmdline resource.
 */
export interface KernelCmdlineArgs {
    /**
     * Parameters that must be on the line, as key=value or bare.
     */
    flags: pulumi.Input<pulumi.Input<string>[]>;
    /**
     * The file, when it is not /boot/firmware/cmdline.txt or /boot/cmdline.txt.
     */
    path?: pulumi.Input<string>;
}

import * as pulumi from "@pulumi/pulumi";
/**
 * A samba share: one section of smb.conf, edited in place and compared against the file.
 */
export declare class SambaShare extends pulumi.CustomResource {
    /**
     * Get an existing SambaShare resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): SambaShare;
    /**
     * Returns true if the given object is an instance of SambaShare.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is SambaShare;
    /**
     * The section as the file says it.
     */
    readonly actual: pulumi.Output<{
        [key: string]: string;
    }>;
    readonly config: pulumi.Output<string>;
    /**
     * Whether testparm can parse the whole file.
     */
    readonly parses: pulumi.Output<boolean>;
    readonly path: pulumi.Output<string>;
    readonly settings: pulumi.Output<{
        [key: string]: string;
    }>;
    readonly share: pulumi.Output<string>;
    /**
     * Create a SambaShare resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: SambaShareArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a SambaShare resource.
 */
export interface SambaShareArgs {
    /**
     * The smb.conf to edit.
     */
    config?: pulumi.Input<string>;
    /**
     * The directory the share serves.
     */
    path: pulumi.Input<string>;
    /**
     * Further parameters of the section, written as given.
     */
    settings?: pulumi.Input<{
        [key: string]: pulumi.Input<string>;
    }>;
    /**
     * The section name, as in [photos].
     */
    share: pulumi.Input<string>;
}

import * as pulumi from "@pulumi/pulumi";
/**
 * A self-updating tool, installed the vendor's way and then left alone.
 */
export declare class OfficialInstall extends pulumi.CustomResource {
    /**
     * Get an existing OfficialInstall resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): OfficialInstall;
    /**
     * Returns true if the given object is an instance of OfficialInstall.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is OfficialInstall;
    readonly account: pulumi.Output<string>;
    readonly bin: pulumi.Output<string>;
    readonly home: pulumi.Output<string>;
    readonly installer: pulumi.Output<string>;
    /**
     * What the tool said its version was when last read. Reported, never enforced.
     */
    readonly version: pulumi.Output<string>;
    /**
     * Create a OfficialInstall resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: OfficialInstallArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a OfficialInstall resource.
 */
export interface OfficialInstallArgs {
    /**
     * The account it is installed for, and run as.
     */
    account: pulumi.Input<string>;
    /**
     * The executable the installer leaves behind. Its presence is what installed means.
     */
    bin: pulumi.Input<string>;
    /**
     * That account's home, which the installers write under.
     */
    home: pulumi.Input<string>;
    /**
     * The vendor's install command, run by bash as the account with HOME set: for example `curl -fsSL https://claude.ai/install.sh | bash`.
     */
    installer: pulumi.Input<string>;
}

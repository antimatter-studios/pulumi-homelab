import * as pulumi from "@pulumi/pulumi";
import * as inputs from "./types/input";
import * as outputs from "./types/output";
/**
 * An sshd drop-in, validated before it is installed.
 */
export declare class SshdConfig extends pulumi.CustomResource {
    /**
     * Get an existing SshdConfig resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): SshdConfig;
    /**
     * Returns true if the given object is an instance of SshdConfig.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is SshdConfig;
    readonly config: pulumi.Output<string>;
    readonly directory: pulumi.Output<string>;
    /**
     * What sshd -T resolves, every file merged.
     */
    readonly effective: pulumi.Output<{
        [key: string]: string;
    }>;
    readonly file: pulumi.Output<string>;
    readonly match: pulumi.Output<{
        [key: string]: string;
    }>;
    /**
     * Keywords of this drop-in that sshd resolves differently.
     */
    readonly overridden: pulumi.Output<string[]>;
    readonly rendered: pulumi.Output<{
        [key: string]: string;
    }>;
    /**
     * The rendered keywords and values.
     */
    readonly settings: pulumi.Output<outputs.provider.SshdSettings | undefined>;
    readonly unchecked: pulumi.Output<{
        [key: string]: string;
    } | undefined>;
    /**
     * Create a SshdConfig resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args?: SshdConfigArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a SshdConfig resource.
 */
export interface SshdConfigArgs {
    /**
     * The main configuration, /etc/ssh/sshd_config by default.
     */
    config?: pulumi.Input<string>;
    /**
     * The drop-in directory, /etc/ssh/sshd_config.d by default. sshd_config must include it.
     */
    directory?: pulumi.Input<string>;
    /**
     * The drop-in's name without .conf. Defaults to 90-<resource name>.
     */
    file?: pulumi.Input<string>;
    /**
     * Criteria scoping the settings to a Match block.
     */
    match?: pulumi.Input<inputs.provider.SshdMatchArgs>;
    /**
     * Typed settings.
     */
    settings?: pulumi.Input<inputs.provider.SshdSettingsArgs>;
    /**
     * Keywords the typed settings do not name, written as given. One that repeats a typed keyword wins.
     */
    unchecked?: pulumi.Input<{
        [key: string]: pulumi.Input<string>;
    }>;
}

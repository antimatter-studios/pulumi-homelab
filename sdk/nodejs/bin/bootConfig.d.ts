import * as pulumi from "@pulumi/pulumi";
import * as inputs from "./types/input";
/**
 * Lines in one section of a Raspberry Pi's config.txt. Takes effect at the next boot.
 */
export declare class BootConfig extends pulumi.CustomResource {
    /**
     * Get an existing BootConfig resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): BootConfig;
    /**
     * Returns true if the given object is an instance of BootConfig.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is BootConfig;
    /**
     * What vcgencmd get_config int says the firmware parsed.
     */
    readonly effective: pulumi.Output<{
        [key: string]: string;
    }>;
    readonly overlays: pulumi.Output<string[]>;
    /**
     * Settings the firmware resolved differently.
     */
    readonly overridden: pulumi.Output<string[]>;
    readonly path: pulumi.Output<string>;
    readonly section: pulumi.Output<string>;
    readonly settings: pulumi.Output<{
        [key: string]: string;
    }>;
    readonly unchecked: pulumi.Output<{
        [key: string]: string;
    } | undefined>;
    /**
     * Create a BootConfig resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args?: BootConfigArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a BootConfig resource.
 */
export interface BootConfigArgs {
    /**
     * Whole lines, such as dtoverlay=dwc2,dr_mode=host, kept as they are; overlay names are checked against the machine.
     */
    overlays?: pulumi.Input<pulumi.Input<string>[]>;
    /**
     * config.txt, when it is not /boot/firmware/config.txt or /boot/config.txt.
     */
    path?: pulumi.Input<string>;
    /**
     * The filter section the lines go in, all by default.
     */
    section?: pulumi.Input<string>;
    /**
     * Typed settings; booleans are written as 1 and 0.
     */
    settings?: pulumi.Input<inputs.provider.BootSettingsArgs>;
    /**
     * Any other key=value, written as given. One naming a typed key wins.
     */
    unchecked?: pulumi.Input<{
        [key: string]: pulumi.Input<string>;
    }>;
}

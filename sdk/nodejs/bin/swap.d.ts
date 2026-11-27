import * as pulumi from "@pulumi/pulumi";
import * as outputs from "./types/output";
/**
 * Whether the machine swaps, now and after the next boot.
 */
export declare class Swap extends pulumi.CustomResource {
    /**
     * Get an existing Swap resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): Swap;
    /**
     * Returns true if the given object is an instance of Swap.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is Swap;
    /**
     * What /proc/swaps says is swapping now.
     */
    readonly active: pulumi.Output<outputs.provider.SwapActive[]>;
    /**
     * The swapfile unit's enablement, or absent when it is not installed.
     */
    readonly dphys: pulumi.Output<string | undefined>;
    readonly enabled: pulumi.Output<boolean>;
    /**
     * The fstab swap lines that will bring swap back at the next boot.
     */
    readonly fstab: pulumi.Output<string[]>;
    readonly fstabFile: pulumi.Output<string>;
    readonly otherSwap: pulumi.Output<string>;
    readonly path: pulumi.Output<string>;
    readonly sizeMb: pulumi.Output<number | undefined>;
    readonly unit: pulumi.Output<string>;
    /**
     * Whether zram swap is configured.
     */
    readonly zram: pulumi.Output<boolean>;
    /**
     * Create a Swap resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: SwapArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a Swap resource.
 */
export interface SwapArgs {
    /**
     * Whether the machine swaps to the declared file.
     */
    enabled: pulumi.Input<boolean>;
    /**
     * The fstab, /etc/fstab by default.
     */
    fstab?: pulumi.Input<string>;
    /**
     * forbid (the default): off means no swap of any kind. allow: off means only the declared path never swaps, and other swap (zram, another disk) is left alone.
     */
    otherSwap?: pulumi.Input<string>;
    /**
     * The swapfile, /var/swap by default.
     */
    path?: pulumi.Input<string>;
    /**
     * The swapfile's size in MB; required when enabled.
     */
    sizeMb?: pulumi.Input<number>;
    /**
     * The swapfile service that is masked when swap is managed here, dphys-swapfile by default.
     */
    unit?: pulumi.Input<string>;
}

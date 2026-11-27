import * as pulumi from "@pulumi/pulumi";
/**
 * An account the machine should have, checked against passwd rather than remembered.
 */
export declare class User extends pulumi.CustomResource {
    /**
     * Get an existing User resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): User;
    /**
     * Returns true if the given object is an instance of User.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is User;
    readonly allowGroupRemoval: pulumi.Output<boolean>;
    readonly createHome: pulumi.Output<boolean>;
    readonly group: pulumi.Output<string | undefined>;
    readonly groups: pulumi.Output<string[]>;
    readonly home: pulumi.Output<string>;
    readonly name: pulumi.Output<string>;
    readonly shell: pulumi.Output<string>;
    readonly uid: pulumi.Output<number | undefined>;
    /**
     * Create a User resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: UserArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a User resource.
 */
export interface UserArgs {
    /**
     * Permission to remove memberships the arguments do not mention. Off by default.
     */
    allowGroupRemoval?: pulumi.Input<boolean>;
    /**
     * Whether a home directory is made. On by default.
     */
    createHome?: pulumi.Input<boolean>;
    /**
     * The primary group's name, which must already exist. Left out, useradd makes a group named after the account.
     */
    group?: pulumi.Input<string>;
    /**
     * Groups it belongs to besides its own. This is the whole list, not additions to it.
     */
    groups?: pulumi.Input<pulumi.Input<string>[]>;
    /**
     * Where its home is.
     */
    home?: pulumi.Input<string>;
    /**
     * The account's name.
     */
    name: pulumi.Input<string>;
    /**
     * The login shell. Required, with no default: /usr/sbin/nologin for something that only runs, the person's real shell otherwise.
     */
    shell: pulumi.Input<string>;
    /**
     * The numeric uid, set when the account is created. An existing account with a different uid is refused rather than renumbered.
     */
    uid?: pulumi.Input<number>;
}

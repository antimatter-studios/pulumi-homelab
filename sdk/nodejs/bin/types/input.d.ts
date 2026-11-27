import * as pulumi from "@pulumi/pulumi";
export declare namespace provider {
    interface AclEntryArgsArgs {
        /**
         * Who the entry is for. Empty or absent for the owning entries, which only defaultEntries may set.
         */
        name?: pulumi.Input<string>;
        /**
         * rwx, r-x, rw, an octal digit... settled to the three-letter form getfacl prints.
         */
        perms: pulumi.Input<string>;
        /**
         * user, group, mask or other. A mask is refused; user, group and other without a name are refused as access entries.
         */
        type: pulumi.Input<string>;
    }
    interface AclNormaliseArgsArgs {
        /**
         * The exact mode to find, as in 2755.
         */
        from: pulumi.Input<string>;
        /**
         * The group whose directories are corrected.
         */
        group: pulumi.Input<string>;
        /**
         * Paths relative to the ACL's path that the walk never enters.
         */
        ignore?: pulumi.Input<pulumi.Input<string>[]>;
        /**
         * The mode to set instead, as in 2775.
         */
        to: pulumi.Input<string>;
    }
    interface JumpArgs {
        address: pulumi.Input<string>;
        hostKeys?: pulumi.Input<pulumi.Input<string>[]>;
        port: pulumi.Input<number>;
        user: pulumi.Input<string>;
    }
    interface TunnelForwardArgs {
        /**
         * A bind address for the far end to accept or ignore.
         */
        bind?: pulumi.Input<string>;
        /**
         * The port on this machine it reaches.
         */
        local: pulumi.Input<number>;
        /**
         * The port published on the far end.
         */
        remote: pulumi.Input<number>;
        /**
         * What the forward is for, written into the unit as a comment.
         */
        what?: pulumi.Input<string>;
    }
}

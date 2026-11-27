export declare namespace provider {
    interface AclEntryActual {
        effective: string;
        name: string;
        perms: string;
        type: string;
    }
    interface AclEntrySettled {
        name: string;
        perms: string;
        type: string;
    }
    interface AclNormaliseArgs {
        /**
         * The exact mode to find, as in 2755.
         */
        from: string;
        /**
         * The group whose directories are corrected.
         */
        group: string;
        /**
         * Paths relative to the ACL's path that the walk never enters.
         */
        ignore?: string[];
        /**
         * The mode to set instead, as in 2775.
         */
        to: string;
    }
    interface Jump {
        address: string;
        hostKeys?: string[];
        port: number;
        user: string;
    }
    interface TunnelForward {
        /**
         * A bind address for the far end to accept or ignore.
         */
        bind?: string;
        /**
         * The port on this machine it reaches.
         */
        local: number;
        /**
         * The port published on the far end.
         */
        remote: number;
        /**
         * What the forward is for, written into the unit as a comment.
         */
        what?: string;
    }
}

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
    interface HostnameEffective {
        hostsLine: string;
        namesHost: boolean;
        static: string;
        transient: string;
    }
    interface Jump {
        address: string;
        hostKeys?: string[];
        port: number;
        user: string;
    }
    interface Source {
        /**
         * For exec and execFile: the program and its arguments, run without a shell. execFile needs a {} somewhere in it.
         */
        argv?: string[];
        /**
         * exec (the material is stdout), execFile (the material is what the command wrote to {}) or file (a file on the deploying machine).
         */
        kind: string;
        /**
         * For file: the path on the deploying machine.
         */
        path?: string;
    }
    interface SshdSettings {
        allowAgentForwarding?: boolean;
        allowGroups?: string[];
        allowTcpForwarding?: string;
        allowUsers?: string[];
        ciphers?: string[];
        clientAliveCountMax?: number;
        clientAliveInterval?: number;
        compression?: string;
        denyGroups?: string[];
        denyUsers?: string[];
        gatewayPorts?: string;
        ignoreRhosts?: boolean;
        kbdInteractiveAuthentication?: boolean;
        kexAlgorithms?: string[];
        listenAddress?: string[];
        logLevel?: string;
        loginGraceTime?: number;
        macs?: string[];
        maxAuthTries?: number;
        maxSessions?: number;
        maxStartups?: string;
        passwordAuthentication?: boolean;
        permitEmptyPasswords?: boolean;
        permitRootLogin?: string;
        port?: number;
        printMotd?: boolean;
        pubkeyAuthentication?: boolean;
        strictModes?: boolean;
        tcpKeepAlive?: boolean;
        useDNS?: boolean;
        usePAM?: boolean;
        x11Forwarding?: boolean;
    }
    interface SwapActive {
        kind: string;
        path: string;
        priority: number;
        sizeMb: number;
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

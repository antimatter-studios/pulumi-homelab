export declare namespace provider {
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
}

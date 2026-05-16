// SDK configuration

export interface SDKConfig {
  apiEndpoint: string;
  pollIntervalMs: number;
  maxPollIntervalMs: number;
  pollBackoffMultiplier: number;
}

const defaultConfig: SDKConfig = {
  apiEndpoint: process.env.FACTORY_API_ENDPOINT || "http://localhost:8080",
  pollIntervalMs: 1000,
  maxPollIntervalMs: 5000,
  pollBackoffMultiplier: 1.5,
};

let currentConfig: SDKConfig = { ...defaultConfig };

export function getConfig(): SDKConfig {
  return currentConfig;
}

export function setConfig(config: Partial<SDKConfig>): void {
  currentConfig = { ...currentConfig, ...config };
}

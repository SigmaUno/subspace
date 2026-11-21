export interface PushKey {
  public_key: string;
}

export interface PushDevice {
  id: string;
  name: string;
  host: string;
  created_at: string;
  expires_at: string | null;
  last_success_at: string | null;
  failures: number;
}

export interface PushDeviceList {
  subscriptions: PushDevice[] | null;
}

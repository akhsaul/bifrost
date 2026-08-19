import { baseApi } from "./baseApi";

export interface DopplerAuthEntity {
	type?: string;
	name?: string;
	slug?: string;
	workplace?: {
		name?: string;
		id?: string;
	};
	token?: {
		name?: string;
		type?: string;
		created_at?: string;
	};
}

export interface VaultDopplerStatusResponse {
	enabled: boolean;
	type?: string;
	prefix?: string;
	access_mode?: "read_only" | "read_and_write";
	project?: string;
	config?: string;
	base_url?: string;
	connected?: boolean;
	authenticated_entity?: DopplerAuthEntity;
	message?: string;
	error?: string;
}

export interface FlushVaultCacheResponse {
	message: string;
	success: boolean;
}

export const vaultApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		getVaultDopplerStatus: builder.query<VaultDopplerStatusResponse, void>({
			query: () => ({
				url: "/vault/doppler/status",
			}),
			providesTags: ["Config"],
		}),

		flushVaultCache: builder.mutation<FlushVaultCacheResponse, void>({
			query: () => ({
				url: "/vault/flush-cache",
				method: "POST",
			}),
			invalidatesTags: ["Config"],
		}),
	}),
});

export const { useGetVaultDopplerStatusQuery, useFlushVaultCacheMutation } = vaultApi;

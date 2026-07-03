package net.anzix.acpp.data.remote

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import okhttp3.OkHttpClient
import okhttp3.Request
import java.io.IOException
import javax.inject.Inject
import javax.inject.Named

/**
 * Validates a server URL + optional credential during Setup by hitting
 * `GET /api/health`. Uses the plain client (no host/auth interceptors) because
 * the target URL isn't persisted yet.
 */
class ConnectivityChecker @Inject constructor(
    @Named("plain") private val client: OkHttpClient,
) {
    suspend fun check(baseUrl: String, token: String): Result<Unit> = withContext(Dispatchers.IO) {
        val base = baseUrl.toHttpUrlOrNull()
            ?: return@withContext Result.failure(IllegalArgumentException("Invalid URL"))
        val url = base.newBuilder().addPathSegments("api/health").build()
        val builder = Request.Builder().url(url).get()
        if (token.isNotBlank()) builder.addHeader("Authorization", "Bearer $token")
        try {
            client.newCall(builder.build()).execute().use { resp ->
                if (resp.isSuccessful) {
                    Result.success(Unit)
                } else {
                    Result.failure(IOException("Server returned HTTP ${resp.code}"))
                }
            }
        } catch (e: Exception) {
            Result.failure(e)
        }
    }
}

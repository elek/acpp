package net.anzix.acpp.data.remote

import net.anzix.acpp.data.settings.SettingsRepository
import kotlinx.coroutines.runBlocking
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import okhttp3.Interceptor
import okhttp3.Response
import javax.inject.Inject

/**
 * Rewrites each request's scheme/host/port to the configured server base URL.
 * Retrofit is built with a placeholder base URL; this lets the target change at
 * runtime (after Setup) without rebuilding Retrofit.
 */
class HostSelectionInterceptor @Inject constructor(
    private val settings: SettingsRepository,
) : Interceptor {
    override fun intercept(chain: Interceptor.Chain): Response {
        val base = runBlocking { settings.current() }.baseUrl
        val newBase = base.toHttpUrlOrNull()
        var request = chain.request()
        if (newBase != null) {
            val url = request.url.newBuilder()
                .scheme(newBase.scheme)
                .host(newBase.host)
                .port(newBase.port)
                .build()
            request = request.newBuilder().url(url).build()
        }
        return chain.proceed(request)
    }
}

/** Adds `Authorization: Bearer <token>` when a credential is configured. */
class AuthInterceptor @Inject constructor(
    private val settings: SettingsRepository,
) : Interceptor {
    override fun intercept(chain: Interceptor.Chain): Response {
        val token = runBlocking { settings.current() }.token
        val request = if (token.isNotBlank()) {
            chain.request().newBuilder()
                .addHeader("Authorization", "Bearer $token")
                .build()
        } else {
            chain.request()
        }
        return chain.proceed(request)
    }
}

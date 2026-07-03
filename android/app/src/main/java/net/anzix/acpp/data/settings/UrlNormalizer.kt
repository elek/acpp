package net.anzix.acpp.data.settings

import okhttp3.HttpUrl.Companion.toHttpUrlOrNull

/** Pure URL normalization/validation for the Setup screen. */
object UrlNormalizer {
    /**
     * Normalizes a user-entered server URL: trims, defaults the scheme to http,
     * and guarantees a trailing slash so relative API paths resolve correctly.
     * Returns null when the input cannot be parsed as a valid http(s) URL.
     */
    fun normalize(raw: String): String? {
        var s = raw.trim()
        if (s.isEmpty()) return null
        if (!s.startsWith("http://", ignoreCase = true) &&
            !s.startsWith("https://", ignoreCase = true)
        ) {
            s = "http://$s"
        }
        val url = s.toHttpUrlOrNull() ?: return null
        if (url.host.isBlank()) return null
        // HttpUrl.toString() always renders at least a "/" path.
        return url.toString()
    }
}

package net.anzix.acpp.data.settings

/** Persisted client configuration. */
data class AppConfig(
    val baseUrl: String = "",
    val token: String = "",
    val configured: Boolean = false,
)

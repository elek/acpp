package net.anzix.acpp.data.settings

import android.content.Context
import androidx.datastore.preferences.core.booleanPreferencesKey
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import dagger.hilt.android.qualifiers.ApplicationContext
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.map
import javax.inject.Inject
import javax.inject.Singleton

private val Context.dataStore by preferencesDataStore(name = "acpp_settings")

@Singleton
class SettingsRepository @Inject constructor(
    @ApplicationContext private val context: Context,
) {
    private object Keys {
        val BASE_URL = stringPreferencesKey("base_url")
        val TOKEN = stringPreferencesKey("token")
        val CONFIGURED = booleanPreferencesKey("configured")
    }

    val config: Flow<AppConfig> = context.dataStore.data.map { p ->
        AppConfig(
            baseUrl = p[Keys.BASE_URL] ?: "",
            token = p[Keys.TOKEN] ?: "",
            configured = p[Keys.CONFIGURED] ?: false,
        )
    }

    /** Latest snapshot; used by network interceptors on background threads. */
    suspend fun current(): AppConfig = config.first()

    suspend fun save(baseUrl: String, token: String) {
        context.dataStore.edit {
            it[Keys.BASE_URL] = baseUrl
            it[Keys.TOKEN] = token
            it[Keys.CONFIGURED] = true
        }
    }

    suspend fun clear() {
        context.dataStore.edit { it.clear() }
    }
}

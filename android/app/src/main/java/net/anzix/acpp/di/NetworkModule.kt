package net.anzix.acpp.di

import net.anzix.acpp.data.remote.AuthInterceptor
import net.anzix.acpp.data.remote.EventReducer
import net.anzix.acpp.data.remote.AcppApi
import net.anzix.acpp.data.remote.HostSelectionInterceptor
import dagger.Module
import dagger.Provides
import dagger.hilt.InstallIn
import dagger.hilt.components.SingletonComponent
import kotlinx.serialization.json.Json
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.logging.HttpLoggingInterceptor
import retrofit2.Retrofit
import retrofit2.converter.kotlinx.serialization.asConverterFactory
import javax.inject.Named
import javax.inject.Singleton

@Module
@InstallIn(SingletonComponent::class)
object NetworkModule {

    @Provides
    @Singleton
    fun provideJson(): Json = Json {
        ignoreUnknownKeys = true
        explicitNulls = false
        coerceInputValues = true
    }

    @Provides
    @Singleton
    fun provideLogging(): HttpLoggingInterceptor =
        HttpLoggingInterceptor().apply { level = HttpLoggingInterceptor.Level.BASIC }

    @Provides
    @Singleton
    @Named("plain")
    fun providePlainClient(logging: HttpLoggingInterceptor): OkHttpClient =
        OkHttpClient.Builder()
            .addInterceptor(logging)
            .build()

    @Provides
    @Singleton
    @Named("api")
    fun provideApiClient(
        host: HostSelectionInterceptor,
        auth: AuthInterceptor,
        logging: HttpLoggingInterceptor,
    ): OkHttpClient =
        OkHttpClient.Builder()
            .followRedirects(false) // keep POST /stop as a raw 303
            .addInterceptor(host)
            .addInterceptor(auth)
            .addInterceptor(logging)
            .build()

    @Provides
    @Singleton
    fun provideRetrofit(
        @Named("api") client: OkHttpClient,
        json: Json,
    ): Retrofit =
        Retrofit.Builder()
            .baseUrl("http://localhost/") // placeholder; rewritten by HostSelectionInterceptor
            .client(client)
            .addConverterFactory(json.asConverterFactory("application/json".toMediaType()))
            .build()

    @Provides
    @Singleton
    fun provideAcppApi(retrofit: Retrofit): AcppApi =
        retrofit.create(AcppApi::class.java)

    @Provides
    @Singleton
    fun provideEventReducer(json: Json): EventReducer = EventReducer(json)
}

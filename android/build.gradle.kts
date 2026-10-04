plugins {
    id("com.android.application") version "9.4.1" apply false
    // AGP compiles Kotlin itself (built-in Kotlin); this only picks the
    // Kotlin Gradle plugin version it uses.
    id("org.jetbrains.kotlin.android") version "2.4.20" apply false
}

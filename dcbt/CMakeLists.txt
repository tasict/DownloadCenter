# dc-bt: the libtorrent torrent engine of Download Center (static build).
cmake_minimum_required(VERSION 3.16)
project(dc-bt CXX)
set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
find_package(LibtorrentRasterbar REQUIRED)
add_executable(dc-bt dc-bt.cpp)
target_include_directories(dc-bt PRIVATE ${JSON_INCLUDE_DIR})
target_compile_options(dc-bt PRIVATE -fPIE -fstack-protector-strong)
target_compile_definitions(dc-bt PRIVATE _FORTIFY_SOURCE=2)
target_link_libraries(dc-bt PRIVATE LibtorrentRasterbar::torrent-rasterbar)
# A static PIE (loaded at a random address) with full RELRO; every library
# it links is built position independent (tools/dcbt/build-in-container.sh)
target_link_options(dc-bt PRIVATE -static-pie -Wl,-z,relro,-z,now)

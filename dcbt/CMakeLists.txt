# dc-bt: the libtorrent torrent engine of Download Center (static build).
cmake_minimum_required(VERSION 3.16)
project(dc-bt CXX)
set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
find_package(LibtorrentRasterbar REQUIRED)
add_executable(dc-bt dc-bt.cpp)
target_include_directories(dc-bt PRIVATE ${JSON_INCLUDE_DIR})
target_link_libraries(dc-bt PRIVATE LibtorrentRasterbar::torrent-rasterbar)
# -no-pie: a plain static executable (ET_EXEC) even with PIE-by-default
# cross compilers, which would otherwise produce an untested static-pie
target_link_options(dc-bt PRIVATE -static -no-pie -s)

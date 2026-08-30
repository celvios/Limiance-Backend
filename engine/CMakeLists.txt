cmake_minimum_required(VERSION 3.28)
project(limiance_matching_engine VERSION 1.0.0 LANGUAGES CXX)

find_package(flatbuffers 25.12.19 CONFIG REQUIRED)

find_package(PkgConfig REQUIRED)
pkg_check_modules(ZMQ REQUIRED IMPORTED_TARGET libzmq)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

option(LIMIANCE_ENABLE_SANITIZERS "Enable address and undefined behavior sanitizers" OFF)
option(LIMIANCE_ENABLE_COVERAGE "Enable GCC coverage instrumentation" OFF)
if(LIMIANCE_ENABLE_SANITIZERS AND CMAKE_CXX_COMPILER_ID MATCHES "GNU|Clang")
  add_compile_options(-fsanitize=address,undefined -fno-omit-frame-pointer)
  add_link_options(-fsanitize=address,undefined)
endif()

add_library(limiance_engine_core STATIC
  src/order_book.cpp
  src/journal.cpp
  src/protocol.cpp
  src/symbol_engine.cpp
)
target_include_directories(limiance_engine_core PUBLIC include ../schema/generated/cpp)
target_link_libraries(limiance_engine_core PUBLIC flatbuffers::flatbuffers)
target_compile_options(limiance_engine_core PRIVATE -Wall -Wextra -Wconversion -Werror)
if(LIMIANCE_ENABLE_COVERAGE AND CMAKE_CXX_COMPILER_ID STREQUAL "GNU")
  target_compile_options(limiance_engine_core PRIVATE -O0 --coverage)
  target_link_options(limiance_engine_core INTERFACE --coverage)
endif()

add_executable(matching_engine src/main.cpp)
target_link_libraries(matching_engine PRIVATE limiance_engine_core PkgConfig::ZMQ)
target_compile_options(matching_engine PRIVATE -Wall -Wextra -Wconversion -Werror)

include(CTest)
if(BUILD_TESTING)
  find_package(GTest REQUIRED)
  add_executable(matching_engine_test
    tests/order_book_test.cpp
    tests/journal_test.cpp
    tests/symbol_engine_test.cpp
  )
  target_link_libraries(matching_engine_test PRIVATE limiance_engine_core GTest::gtest_main)
  include(GoogleTest)
  gtest_discover_tests(matching_engine_test)

  add_executable(trading_wire_contract_test ../schema/tests/wire_contract_test.cpp)
  target_link_libraries(trading_wire_contract_test PRIVATE flatbuffers::flatbuffers)
  add_test(NAME trading_wire_contract COMMAND trading_wire_contract_test)

  add_executable(control_wire_contract_test ../schema/tests/control_wire_contract_test.cpp)
  target_link_libraries(control_wire_contract_test PRIVATE flatbuffers::flatbuffers)
  add_test(NAME control_wire_contract COMMAND control_wire_contract_test)

  if(UNIX)
    add_executable(engine_transport_test tests/transport_test.cpp)
    target_include_directories(engine_transport_test PRIVATE ../schema/generated/cpp)
    target_link_libraries(engine_transport_test PRIVATE flatbuffers::flatbuffers PkgConfig::ZMQ GTest::gtest_main)
    add_dependencies(engine_transport_test matching_engine)
    gtest_discover_tests(engine_transport_test)
  endif()
endif()
